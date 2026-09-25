// Package reviewcontext turns an ai-doc-template review and its current HTML
// document into a small, resolved context intended for an AI agent.
package reviewcontext

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const (
	reviewSchemaVersion = 1
	maxReviewBytes      = 1 << 20
	maxQuoteBytes       = 32000
	maxTargetBytes      = 2000
	maxContextBytes     = 400
	maxReviewIDBytes    = 220
)

// SourceStatus says whether the current HTML bytes are the bytes that were
// reviewed.
type SourceStatus string

const (
	SourceCurrent SourceStatus = "current"
	SourceChanged SourceStatus = "changed"
)

// MatchStatus describes how confidently a saved comment was mapped to the
// current document.
type MatchStatus string

const (
	MatchExact     MatchStatus = "exact"
	MatchRelocated MatchStatus = "relocated"
	MatchChanged   MatchStatus = "changed"
	MatchAmbiguous MatchStatus = "ambiguous"
	MatchMissing   MatchStatus = "missing"
)

// Result is the compact, agent-facing representation of a review.
type Result struct {
	Document     string            `json:"document"`
	SourceStatus SourceStatus      `json:"sourceStatus"`
	Comments     []ResolvedComment `json:"comments"`
}

// ResolvedComment contains only the current context needed to act on one
// human review comment.
type ResolvedComment struct {
	Status         MatchStatus `json:"status"`
	TargetID       string      `json:"targetId,omitempty"`
	Section        []string    `json:"section,omitempty"`
	Before         string      `json:"before,omitempty"`
	ReviewedTarget string      `json:"reviewedTarget,omitempty"`
	Target         string      `json:"target,omitempty"`
	After          string      `json:"after,omitempty"`
	Comment        string      `json:"comment"`
	CandidateCount int         `json:"candidateCount,omitempty"`
}

type reviewDocument struct {
	SchemaVersion int            `json:"schemaVersion"`
	Source        reviewSource   `json:"source"`
	Review        reviewMetadata `json:"review"`
	Comments      []reviewEntry  `json:"comments"`
}

type reviewSource struct {
	Path       string `json:"path"`
	URL        string `json:"url,omitempty"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size,omitempty"`
	ModifiedAt string `json:"modifiedAt,omitempty"`
	Title      string `json:"title,omitempty"`
}

type reviewMetadata struct {
	Path        string `json:"path"`
	URL         string `json:"url,omitempty"`
	Revision    string `json:"revision,omitempty"`
	SubmittedAt string `json:"submittedAt,omitempty"`
}

type reviewEntry struct {
	ID        string      `json:"id"`
	Block     reviewBlock `json:"block"`
	Comment   string      `json:"comment"`
	CreatedAt string      `json:"createdAt"`
	UpdatedAt string      `json:"updatedAt"`
}

type reviewBlock struct {
	ID                    string `json:"id"`
	SectionID             string `json:"sectionId"`
	ParagraphIndex        int    `json:"paragraphIndex"`
	SectionParagraphIndex int    `json:"sectionParagraphIndex"`
	Quote                 string `json:"quote"`
	TextSHA256            string `json:"textSHA256"`
}

type currentBlock struct {
	id      string
	text    string
	hash    string
	section []string
}

// Analyze reads documentPath and its sibling .review.json file, resolves all
// comments against the current HTML, and returns the compact context.
func Analyze(documentPath string) (Result, error) {
	if strings.TrimSpace(documentPath) == "" {
		return Result{}, errors.New("document path is required")
	}
	htmlBytes, err := os.ReadFile(documentPath)
	if err != nil {
		return Result{}, fmt.Errorf("read HTML: %w", err)
	}
	reviewPath := documentPath + ".review.json"
	reviewBytes, err := os.ReadFile(reviewPath)
	if err != nil {
		return Result{}, fmt.Errorf("read review %q: %w", reviewPath, err)
	}
	return analyzeBytes(htmlBytes, reviewBytes)
}

func analyzeBytes(htmlBytes, reviewBytes []byte) (Result, error) {
	review, err := decodeReview(reviewBytes)
	if err != nil {
		return Result{}, err
	}
	document, err := html.Parse(bytes.NewReader(htmlBytes))
	if err != nil {
		return Result{}, fmt.Errorf("parse HTML: %w", err)
	}
	blocks, title, err := extractBlocks(document)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(review.Source.Title) != "" {
		title = normalizeText(review.Source.Title)
	}
	if title == "" {
		title = review.Source.Path
	}

	result := Result{
		Document:     title,
		SourceStatus: SourceChanged,
		Comments:     make([]ResolvedComment, 0, len(review.Comments)),
	}
	if hashBytes(htmlBytes) == review.Source.SHA256 {
		result.SourceStatus = SourceCurrent
	}

	byID := make(map[string]int, len(blocks))
	byHash := make(map[string][]int, len(blocks))
	for i, block := range blocks {
		byID[block.id] = i
		byHash[block.hash] = append(byHash[block.hash], i)
	}
	for _, entry := range review.Comments {
		result.Comments = append(result.Comments, resolveComment(entry, blocks, byID, byHash))
	}
	return result, nil
}

func decodeReview(contents []byte) (reviewDocument, error) {
	if len(contents) > maxReviewBytes {
		return reviewDocument{}, errors.New("review exceeds 1 MiB")
	}
	var review reviewDocument
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&review); err != nil {
		return reviewDocument{}, fmt.Errorf("decode review: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return reviewDocument{}, errors.New("decode review: trailing JSON value")
		}
		return reviewDocument{}, fmt.Errorf("decode review: %w", err)
	}
	if review.SchemaVersion != reviewSchemaVersion {
		return reviewDocument{}, fmt.Errorf("unsupported review schemaVersion %d", review.SchemaVersion)
	}
	if !validSHA256(review.Source.SHA256) {
		return reviewDocument{}, errors.New("review source.sha256 is invalid")
	}
	for i := range review.Comments {
		entry := &review.Comments[i]
		entry.Block.ID = strings.TrimSpace(entry.Block.ID)
		entry.Block.Quote = normalizeText(entry.Block.Quote)
		entry.Comment = strings.TrimSpace(entry.Comment)
		if entry.Block.ID == "" || entry.Block.Quote == "" || entry.Comment == "" {
			return reviewDocument{}, fmt.Errorf("review comments[%d] is incomplete", i)
		}
		expected := hashBytes([]byte(entry.Block.Quote))
		if entry.Block.TextSHA256 == "" {
			entry.Block.TextSHA256 = expected
		} else if !validSHA256(entry.Block.TextSHA256) || entry.Block.TextSHA256 != expected {
			return reviewDocument{}, fmt.Errorf("review comments[%d].block.textSHA256 does not match quote", i)
		}
	}
	return review, nil
}

func resolveComment(entry reviewEntry, blocks []currentBlock, byID map[string]int, byHash map[string][]int) ResolvedComment {
	resolved := ResolvedComment{
		Status:         MatchMissing,
		ReviewedTarget: clipText(entry.Block.Quote, maxTargetBytes),
		Comment:        entry.Comment,
	}
	idIndex, hasID := byID[entry.Block.ID]
	hashMatches := byHash[entry.Block.TextSHA256]

	switch {
	case hasID && blocks[idIndex].hash == entry.Block.TextSHA256:
		resolved.Status = MatchExact
		populateContext(&resolved, blocks, idIndex)
		resolved.ReviewedTarget = ""
	case len(hashMatches) == 1:
		resolved.Status = MatchRelocated
		populateContext(&resolved, blocks, hashMatches[0])
		resolved.ReviewedTarget = ""
	case hasID:
		resolved.Status = MatchChanged
		populateContext(&resolved, blocks, idIndex)
	case len(hashMatches) > 1:
		resolved.Status = MatchAmbiguous
		resolved.CandidateCount = len(hashMatches)
	}
	return resolved
}

func populateContext(resolved *ResolvedComment, blocks []currentBlock, index int) {
	block := blocks[index]
	resolved.TargetID = block.id
	resolved.Section = append([]string(nil), block.section...)
	resolved.Target = clipText(block.text, maxTargetBytes)
	if index > 0 {
		resolved.Before = clipText(blocks[index-1].text, maxContextBytes)
	}
	if index+1 < len(blocks) {
		resolved.After = clipText(blocks[index+1].text, maxContextBytes)
	}
}

func extractBlocks(document *html.Node) ([]currentBlock, string, error) {
	content := firstElement(document, func(node *html.Node) bool { return hasClass(node, "doc-body") })
	if content == nil {
		content = firstElement(document, func(node *html.Node) bool { return node.Data == "body" })
	}
	if content == nil {
		return nil, "", errors.New("HTML has no body element")
	}

	usedHeadingIDs := map[string]bool{}
	walkElements(document, func(node *html.Node) {
		if id := attribute(node, "id"); id != "" {
			usedHeadingIDs[id] = true
		}
	})
	headingNumber := 0
	walkElements(content, func(node *html.Node) {
		if node.Data != "h2" && node.Data != "h3" && node.Data != "h4" {
			return
		}
		headingNumber++
		if attribute(node, "id") != "" {
			return
		}
		base := slugify(textContent(node, false))
		if base == "" {
			base = fmt.Sprintf("section-%d", headingNumber)
		}
		id := base
		for duplicate := 2; usedHeadingIDs[id]; duplicate++ {
			id = fmt.Sprintf("%s-%d", base, duplicate)
		}
		usedHeadingIDs[id] = true
		setAttribute(node, "id", id)
	})

	var targets []*html.Node
	walkElements(content, func(node *html.Node) {
		if node.Data != "p" && node.Data != "li" {
			return
		}
		if node.Data == "li" && hasDescendant(node, "p") {
			return
		}
		targets = append(targets, node)
	})
	targetIndex := make(map[*html.Node]int, len(targets))
	for i, target := range targets {
		targetIndex[target] = i
	}

	blocks := make([]currentBlock, 0, len(targets))
	currentSectionID := "preamble"
	var currentH2, currentH3 string
	sectionIndexes := map[string]int{}
	usedBlockIDs := map[string]bool{}
	walkElements(content, func(node *html.Node) {
		switch node.Data {
		case "h2", "h3":
			currentSectionID = clippedReviewID(firstNonEmpty(attribute(node, "id"), textContent(node, false)))
			sectionIndexes = map[string]int{}
			heading := normalizeText(textContent(node, false))
			if node.Data == "h2" {
				currentH2, currentH3 = heading, ""
			} else {
				currentH3 = heading
			}
			return
		case "p", "li":
		default:
			return
		}
		if _, ok := targetIndex[node]; !ok {
			return
		}
		elementType := node.Data
		sectionIndex := sectionIndexes[elementType]
		baseID := attribute(node, "data-review-id")
		if baseID == "" {
			baseID = fmt.Sprintf("%s::%s-%d", currentSectionID, elementType, sectionIndex)
		}
		baseID = clippedReviewID(baseID)
		id := baseID
		for duplicate := 2; usedBlockIDs[id]; duplicate++ {
			id = clippedReviewID(fmt.Sprintf("%s-%d", baseID, duplicate))
		}
		usedBlockIDs[id] = true
		text := clipUTF8(normalizeText(textContent(node, node.Data == "li")), maxQuoteBytes, "")
		sectionIndexes[elementType] = sectionIndex + 1
		if text == "" {
			return
		}
		section := make([]string, 0, 2)
		if currentH2 != "" {
			section = append(section, currentH2)
		}
		if currentH3 != "" {
			section = append(section, currentH3)
		}
		blocks = append(blocks, currentBlock{id: id, text: text, hash: hashBytes([]byte(text)), section: section})
	})

	title := ""
	if titleNode := firstElement(document, func(node *html.Node) bool { return node.Data == "title" }); titleNode != nil {
		title = normalizeText(textContent(titleNode, false))
	}
	return blocks, title, nil
}

func walkElements(node *html.Node, visit func(*html.Node)) {
	if node.Type == html.ElementNode {
		visit(node)
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkElements(child, visit)
	}
}

func firstElement(node *html.Node, predicate func(*html.Node) bool) *html.Node {
	if node.Type == html.ElementNode && predicate(node) {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := firstElement(child, predicate); found != nil {
			return found
		}
	}
	return nil
}

func hasDescendant(node *html.Node, tag string) bool {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == tag {
			return true
		}
		if hasDescendant(child, tag) {
			return true
		}
	}
	return false
}

func textContent(node *html.Node, skipNestedLists bool) string {
	var builder strings.Builder
	var write func(*html.Node, bool)
	write = func(current *html.Node, root bool) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
			return
		}
		if !root && skipNestedLists && current.Type == html.ElementNode && (current.Data == "ol" || current.Data == "ul") {
			return
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			write(child, false)
		}
	}
	write(node, true)
	return builder.String()
}

func attribute(node *html.Node, key string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == key {
			return attribute.Val
		}
	}
	return ""
}

func setAttribute(node *html.Node, key, value string) {
	for i := range node.Attr {
		if node.Attr[i].Key == key {
			node.Attr[i].Val = value
			return
		}
	}
	node.Attr = append(node.Attr, html.Attribute{Key: key, Val: value})
}

func hasClass(node *html.Node, className string) bool {
	for class := range strings.FieldsSeq(attribute(node, "class")) {
		if class == className {
			return true
		}
	}
	return false
}

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastHyphen := false
	for _, r := range value {
		switch {
		case isSlugSpace(r):
			if builder.Len() > 0 && !lastHyphen {
				builder.WriteByte('-')
				lastHyphen = true
			}
		case strings.ContainsRune(`!"#$%&'()*+,./:;<=>?@[\]^`+"`"+`{|}~。、，．・「」『』（）〔〕【】〈〉《》！？：；`, r):
			continue
		default:
			builder.WriteRune(r)
			lastHyphen = false
		}
	}
	return strings.Trim(builder.String(), "-")
}

func isSlugSpace(r rune) bool {
	return unicode.IsSpace(r)
}

func normalizeText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func clippedReviewID(value string) string {
	value = clipUTF8(normalizeText(value), maxReviewIDBytes, "")
	if value == "" {
		return "preamble"
	}
	return value
}

func clipText(value string, maxBytes int) string {
	return clipUTF8(value, maxBytes, "…")
}

func clipUTF8(value string, maxBytes int, suffix string) string {
	if len(value) <= maxBytes {
		return value
	}
	limit := maxBytes - len(suffix)
	if limit < 0 {
		limit = 0
	}
	end := 0
	for end < len(value) {
		_, size := utf8.DecodeRuneInString(value[end:])
		if end+size > limit {
			break
		}
		end += size
	}
	return value[:end] + suffix
}

func hashBytes(contents []byte) string {
	digest := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validSHA256(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
