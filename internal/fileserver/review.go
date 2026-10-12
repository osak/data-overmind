package fileserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	reviewAPIPrefix         = "/_data-overmind/"
	reviewContextPath       = reviewAPIPrefix + "review-context"
	reviewSavePath          = reviewAPIPrefix + "reviews"
	reviewSchemaVersion     = 1
	maxReviewBodyBytes      = 1 << 20
	maxReviewComments       = 500
	maxReviewCommentBytes   = 16 << 10
	maxReviewQuoteBytes     = 32 << 10
	maxReviewIdentifierSize = 256
	maxReviewTitleBytes     = 1 << 10
)

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

type reviewBlock struct {
	ID                    string `json:"id"`
	SectionID             string `json:"sectionId"`
	ParagraphIndex        int    `json:"paragraphIndex"`
	SectionParagraphIndex int    `json:"sectionParagraphIndex"`
	Quote                 string `json:"quote"`
	TextSHA256            string `json:"textSHA256"`
}

type reviewComment struct {
	ID        string      `json:"id"`
	Block     reviewBlock `json:"block"`
	Comment   string      `json:"comment"`
	CreatedAt string      `json:"createdAt"`
	UpdatedAt string      `json:"updatedAt"`
}

type reviewDocument struct {
	SchemaVersion int             `json:"schemaVersion"`
	Source        reviewSource    `json:"source"`
	Review        reviewMetadata  `json:"review"`
	Comments      []reviewComment `json:"comments"`
}

type reviewResource struct {
	Path     string          `json:"path"`
	URL      string          `json:"url"`
	Revision *string         `json:"revision"`
	Document *reviewDocument `json:"document"`
}

type reviewContextResponse struct {
	SchemaVersion int            `json:"schemaVersion"`
	Source        reviewSource   `json:"source"`
	Review        reviewResource `json:"review"`
}

type reviewSubmitRequest struct {
	SchemaVersion int             `json:"schemaVersion"`
	SourceSHA256  string          `json:"sourceSHA256"`
	BaseRevision  *string         `json:"baseRevision"`
	DocumentTitle string          `json:"documentTitle"`
	Comments      []reviewComment `json:"comments"`
}

type reviewTarget struct {
	SourcePath string
	SourceURL  string
	SourceFull string
	ReviewPath string
	ReviewURL  string
	ReviewFull string
	Info       os.FileInfo
}

type reviewAPIError struct {
	Status  int
	Code    string
	Message string
	Details any
}

func (e *reviewAPIError) Error() string { return e.Message }

func (s *fileServer) serveReviewAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	switch r.URL.Path {
	case reviewContextPath:
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeReviewError(w, &reviewAPIError{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed", Message: "method not allowed"})
			return
		}
		s.serveReviewContext(w, r)
	case reviewSavePath:
		if r.Method != http.MethodPut {
			w.Header().Set("Allow", http.MethodPut)
			writeReviewError(w, &reviewAPIError{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed", Message: "method not allowed"})
			return
		}
		s.serveReviewSave(w, r)
	default:
		writeReviewError(w, &reviewAPIError{Status: http.StatusNotFound, Code: "not_found", Message: "review API endpoint not found"})
	}
}

func (s *fileServer) serveReviewContext(w http.ResponseWriter, r *http.Request) {
	target, apiErr := s.reviewTarget(r.URL.Query().Get("document"))
	if apiErr != nil {
		writeReviewError(w, apiErr)
		return
	}
	response, apiErr := reviewContext(target)
	if apiErr != nil {
		writeReviewError(w, apiErr)
		return
	}
	writeReviewJSON(w, http.StatusOK, response)
}

func (s *fileServer) serveReviewSave(w http.ResponseWriter, r *http.Request) {
	if apiErr := validateReviewRequestHeaders(r); apiErr != nil {
		writeReviewError(w, apiErr)
		return
	}
	target, apiErr := s.reviewTarget(r.URL.Query().Get("document"))
	if apiErr != nil {
		writeReviewError(w, apiErr)
		return
	}

	request, apiErr := decodeReviewSubmit(w, r)
	if apiErr != nil {
		writeReviewError(w, apiErr)
		return
	}
	if apiErr := validateReviewSubmit(&request); apiErr != nil {
		writeReviewError(w, apiErr)
		return
	}

	// Serialize compare-and-swap writes handled by this server. This makes the
	// baseRevision check authoritative for simultaneous browser submissions.
	s.reviewMu.Lock()
	defer s.reviewMu.Unlock()

	sourceContents, err := os.ReadFile(target.SourceFull)
	if err != nil {
		writeReviewError(w, internalReviewError("read source", err))
		return
	}
	currentSourceHash := hashBytes(sourceContents)
	if request.SourceSHA256 != currentSourceHash {
		writeReviewError(w, &reviewAPIError{
			Status:  http.StatusConflict,
			Code:    "source_changed",
			Message: "the source document changed after the review was opened",
			Details: map[string]string{"sourceSHA256": currentSourceHash},
		})
		return
	}

	existingDocument, existingRevision, exists, apiErr := readReviewDocument(target)
	if apiErr != nil {
		writeReviewError(w, apiErr)
		return
	}
	if !matchingRevision(request.BaseRevision, existingRevision, exists) {
		details := map[string]any{"reviewRevision": nil}
		if exists {
			details["reviewRevision"] = existingRevision
		}
		writeReviewError(w, &reviewAPIError{
			Status:  http.StatusConflict,
			Code:    "review_changed",
			Message: "the saved review changed after the review was opened",
			Details: details,
		})
		return
	}
	document := reviewDocument{
		SchemaVersion: reviewSchemaVersion,
		Source: reviewSource{
			Path:   target.SourcePath,
			SHA256: currentSourceHash,
			Title:  strings.TrimSpace(request.DocumentTitle),
		},
		Review: reviewMetadata{
			Path:        target.ReviewPath,
			SubmittedAt: time.Now().UTC().Format(time.RFC3339Nano),
		},
		Comments: request.Comments,
	}
	contents, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		writeReviewError(w, internalReviewError("encode review", err))
		return
	}
	contents = append(contents, '\n')
	if exists {
		previousContents, err := os.ReadFile(target.ReviewFull)
		if err != nil {
			writeReviewError(w, internalReviewError("read previous review", err))
			return
		}
		if hashBytes(previousContents) != existingRevision {
			writeReviewError(w, &reviewAPIError{Status: http.StatusConflict, Code: "review_changed", Message: "the saved review changed during submission"})
			return
		}
		var previousSource []byte
		if existingDocument.Source.SHA256 == currentSourceHash {
			previousSource = sourceContents
		}
		if err := archiveReview(target, existingDocument.Source.SHA256, previousSource, previousContents); err != nil {
			writeReviewError(w, internalReviewError("archive previous review", err))
			return
		}
	}
	if err := archiveReview(target, currentSourceHash, sourceContents, contents); err != nil {
		writeReviewError(w, internalReviewError("archive review", err))
		return
	}

	created, err := atomicWriteReview(target.ReviewFull, contents)
	if err != nil {
		writeReviewError(w, internalReviewError("write review", err))
		return
	}
	revision := hashBytes(contents)
	response, apiErr := reviewContext(target)
	if apiErr != nil {
		writeReviewError(w, apiErr)
		return
	}
	response.Review.Revision = &revision
	response.Review.Document = &document
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeReviewJSON(w, status, response)
}

func (s *fileServer) reviewTarget(document string) (reviewTarget, *reviewAPIError) {
	decoded, err := url.PathUnescape(document)
	if err != nil {
		return reviewTarget{}, badReviewRequest("invalid_document", "document contains invalid URL escaping")
	}
	document = decoded
	if document == "" || !strings.HasPrefix(document, "/") || strings.Contains(document, "\\") || strings.ContainsRune(document, '\x00') {
		return reviewTarget{}, badReviewRequest("invalid_document", "document must be an absolute URL path")
	}
	cleaned := path.Clean(document)
	if cleaned != document || cleaned == "/" {
		return reviewTarget{}, badReviewRequest("invalid_document", "document must be a normalized file URL path")
	}
	rel := strings.TrimPrefix(cleaned, "/")
	fullPath, err := s.resolve(rel)
	if err != nil {
		return reviewTarget{}, &reviewAPIError{Status: http.StatusNotFound, Code: "source_not_found", Message: "source document not found"}
	}
	info, err := os.Stat(fullPath)
	if err != nil || !info.Mode().IsRegular() || !isHTML(info.Name()) {
		return reviewTarget{}, &reviewAPIError{Status: http.StatusNotFound, Code: "source_not_found", Message: "source document must be an HTML file"}
	}

	canonicalRel, err := filepath.Rel(s.realRoot, fullPath)
	if err != nil || canonicalRel == ".." || strings.HasPrefix(canonicalRel, ".."+string(filepath.Separator)) || filepath.IsAbs(canonicalRel) {
		return reviewTarget{}, &reviewAPIError{Status: http.StatusNotFound, Code: "source_not_found", Message: "source document not found"}
	}
	canonicalRel = filepath.ToSlash(canonicalRel)
	for _, segment := range strings.Split(canonicalRel, "/") {
		if strings.HasSuffix(segment, ".review-history") {
			return reviewTarget{}, &reviewAPIError{Status: http.StatusNotFound, Code: "source_not_found", Message: "review history snapshots are read-only"}
		}
	}
	reviewFull := filepath.Join(filepath.Dir(fullPath), filepath.Base(fullPath)+".review.json")
	reviewRel, err := filepath.Rel(s.realRoot, reviewFull)
	if err != nil || reviewRel == ".." || strings.HasPrefix(reviewRel, ".."+string(filepath.Separator)) || filepath.IsAbs(reviewRel) {
		return reviewTarget{}, &reviewAPIError{Status: http.StatusNotFound, Code: "source_not_found", Message: "source document not found"}
	}
	reviewRel = filepath.ToSlash(reviewRel)

	return reviewTarget{
		SourcePath: canonicalRel,
		SourceURL:  rawURL(canonicalRel),
		SourceFull: fullPath,
		ReviewPath: reviewRel,
		ReviewURL:  rawURL(reviewRel),
		ReviewFull: reviewFull,
		Info:       info,
	}, nil
}

func reviewContext(target reviewTarget) (reviewContextResponse, *reviewAPIError) {
	sourceHash, err := hashFile(target.SourceFull)
	if err != nil {
		return reviewContextResponse{}, internalReviewError("hash source", err)
	}
	document, revision, exists, apiErr := readReviewDocument(target)
	if apiErr != nil {
		return reviewContextResponse{}, apiErr
	}
	var revisionPtr *string
	if exists {
		revisionPtr = &revision
	}
	return reviewContextResponse{
		SchemaVersion: reviewSchemaVersion,
		Source: reviewSource{
			Path:       target.SourcePath,
			URL:        target.SourceURL,
			SHA256:     sourceHash,
			Size:       target.Info.Size(),
			ModifiedAt: target.Info.ModTime().Format(time.RFC3339Nano),
		},
		Review: reviewResource{
			Path:     target.ReviewPath,
			URL:      target.ReviewURL,
			Revision: revisionPtr,
			Document: document,
		},
	}, nil
}

func readReviewDocument(target reviewTarget) (*reviewDocument, string, bool, *reviewAPIError) {
	info, err := os.Lstat(target.ReviewFull)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, internalReviewError("stat review", err)
	}
	if !info.Mode().IsRegular() {
		return nil, "", false, &reviewAPIError{Status: http.StatusConflict, Code: "invalid_existing_review", Message: "existing review is not a regular file"}
	}
	if info.Size() > maxReviewBodyBytes {
		return nil, "", false, &reviewAPIError{Status: http.StatusConflict, Code: "invalid_existing_review", Message: "existing review exceeds 1 MiB"}
	}
	contents, err := os.ReadFile(target.ReviewFull)
	if err != nil {
		return nil, "", false, internalReviewError("read review", err)
	}
	var document reviewDocument
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, "", false, &reviewAPIError{Status: http.StatusConflict, Code: "invalid_existing_review", Message: "existing review is not valid review JSON"}
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, "", false, &reviewAPIError{Status: http.StatusConflict, Code: "invalid_existing_review", Message: "existing review contains trailing JSON"}
	}
	if apiErr := validateReviewDocument(&document, target); apiErr != nil {
		apiErr.Status = http.StatusConflict
		apiErr.Code = "invalid_existing_review"
		apiErr.Message = "existing review does not match the review schema"
		return nil, "", false, apiErr
	}
	return &document, hashBytes(contents), true, nil
}

func validateReviewRequestHeaders(r *http.Request) *reviewAPIError {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return &reviewAPIError{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Message: "Content-Type must be application/json"}
	}
	if r.Header.Get("X-Data-Overmind-Review") != "1" {
		return &reviewAPIError{Status: http.StatusForbidden, Code: "missing_review_header", Message: "review request header is required"}
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return nil
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" {
		return &reviewAPIError{Status: http.StatusForbidden, Code: "cross_origin", Message: "cross-origin review requests are not allowed"}
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if parsed.Scheme != scheme || !strings.EqualFold(parsed.Host, r.Host) {
		return &reviewAPIError{Status: http.StatusForbidden, Code: "cross_origin", Message: "cross-origin review requests are not allowed"}
	}
	return nil
}

func decodeReviewSubmit(w http.ResponseWriter, r *http.Request) (reviewSubmitRequest, *reviewAPIError) {
	r.Body = http.MaxBytesReader(w, r.Body, maxReviewBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request reviewSubmitRequest
	if err := decoder.Decode(&request); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return reviewSubmitRequest{}, &reviewAPIError{Status: http.StatusRequestEntityTooLarge, Code: "request_too_large", Message: "review request exceeds 1 MiB"}
		}
		return reviewSubmitRequest{}, badReviewRequest("invalid_json", "request body must be one valid JSON object")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return reviewSubmitRequest{}, badReviewRequest("invalid_json", "request body must contain exactly one JSON object")
	}
	return request, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("extra JSON value")
		}
		return err
	}
	return nil
}

func validateReviewSubmit(request *reviewSubmitRequest) *reviewAPIError {
	if request.SchemaVersion != reviewSchemaVersion {
		return badReviewRequest("invalid_schema_version", "schemaVersion must be 1")
	}
	if !validSHA256(request.SourceSHA256) {
		return badReviewRequest("invalid_source_hash", "sourceSHA256 must be a sha256: digest")
	}
	if request.BaseRevision != nil && !validSHA256(*request.BaseRevision) {
		return badReviewRequest("invalid_base_revision", "baseRevision must be null or a sha256: digest")
	}
	request.DocumentTitle = strings.TrimSpace(request.DocumentTitle)
	if len(request.DocumentTitle) > maxReviewTitleBytes {
		return badReviewRequest("title_too_large", "documentTitle exceeds 1 KiB")
	}
	if len(request.Comments) > maxReviewComments {
		return badReviewRequest("invalid_comments", "comments must contain at most 500 items")
	}
	if request.Comments == nil {
		request.Comments = []reviewComment{}
	}
	seen := make(map[string]struct{}, len(request.Comments))
	for i := range request.Comments {
		comment := &request.Comments[i]
		comment.ID = strings.TrimSpace(comment.ID)
		comment.Comment = strings.TrimSpace(comment.Comment)
		comment.Block.ID = strings.TrimSpace(comment.Block.ID)
		comment.Block.SectionID = strings.TrimSpace(comment.Block.SectionID)
		comment.Block.Quote = normalizeReviewText(comment.Block.Quote)
		if !validIdentifier(comment.ID) || !validIdentifier(comment.Block.ID) || !validIdentifier(comment.Block.SectionID) {
			return badReviewRequest("invalid_identifier", fmt.Sprintf("comments[%d] contains an invalid identifier", i))
		}
		if _, ok := seen[comment.ID]; ok {
			return badReviewRequest("duplicate_comment_id", fmt.Sprintf("comments[%d].id is duplicated", i))
		}
		seen[comment.ID] = struct{}{}
		if comment.Comment == "" || len(comment.Comment) > maxReviewCommentBytes {
			return badReviewRequest("invalid_comment", fmt.Sprintf("comments[%d].comment must be non-empty and at most 16 KiB", i))
		}
		if comment.Block.Quote == "" || len(comment.Block.Quote) > maxReviewQuoteBytes {
			return badReviewRequest("invalid_quote", fmt.Sprintf("comments[%d].block.quote must be non-empty and at most 32 KiB", i))
		}
		if comment.Block.ParagraphIndex < 0 || comment.Block.SectionParagraphIndex < 0 {
			return badReviewRequest("invalid_paragraph_index", fmt.Sprintf("comments[%d] contains a negative paragraph index", i))
		}
		expectedTextHash := hashBytes([]byte(comment.Block.Quote))
		if comment.Block.TextSHA256 != "" && (!validSHA256(comment.Block.TextSHA256) || comment.Block.TextSHA256 != expectedTextHash) {
			return badReviewRequest("invalid_text_hash", fmt.Sprintf("comments[%d].block.textSHA256 does not match quote", i))
		}
		comment.Block.TextSHA256 = expectedTextHash
		createdAt, err := time.Parse(time.RFC3339Nano, comment.CreatedAt)
		if err != nil {
			return badReviewRequest("invalid_timestamp", fmt.Sprintf("comments[%d].createdAt must be RFC 3339", i))
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, comment.UpdatedAt)
		if err != nil || updatedAt.Before(createdAt) {
			return badReviewRequest("invalid_timestamp", fmt.Sprintf("comments[%d].updatedAt must be RFC 3339 and not precede createdAt", i))
		}
	}
	return nil
}

func validateReviewDocument(document *reviewDocument, target reviewTarget) *reviewAPIError {
	if document.SchemaVersion != reviewSchemaVersion || document.Source.Path != target.SourcePath || document.Review.Path != target.ReviewPath || !validSHA256(document.Source.SHA256) {
		return badReviewRequest("invalid_review_document", "review metadata is invalid")
	}
	if document.Comments == nil {
		document.Comments = []reviewComment{}
	}
	request := reviewSubmitRequest{
		SchemaVersion: document.SchemaVersion,
		SourceSHA256:  document.Source.SHA256,
		DocumentTitle: document.Source.Title,
		Comments:      document.Comments,
	}
	if apiErr := validateReviewSubmit(&request); apiErr != nil {
		return apiErr
	}
	if _, err := time.Parse(time.RFC3339Nano, document.Review.SubmittedAt); err != nil {
		return badReviewRequest("invalid_timestamp", "review.submittedAt must be RFC 3339")
	}
	return nil
}

func validIdentifier(value string) bool {
	return value != "" && len(value) <= maxReviewIdentifierSize
}

func validSHA256(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func normalizeReviewText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func matchingRevision(requested *string, current string, exists bool) bool {
	if !exists {
		return requested == nil
	}
	return requested != nil && *requested == current
}

// archiveReview keeps immutable, content-addressed copies of every submitted
// review. A source snapshot is written once per HTML hash. Legacy reviews whose
// source has already changed retain their comments without an unavailable HTML.
func archiveReview(target reviewTarget, sourceHash string, source, review []byte) error {
	if !validSHA256(sourceHash) {
		return errors.New("invalid source hash for review history")
	}
	base := target.SourceFull + ".review-history"
	versionDir := filepath.Join(base, strings.TrimPrefix(sourceHash, "sha256:"))
	reviewsDir := filepath.Join(versionDir, "reviews")
	for _, dir := range []string{base, versionDir, reviewsDir} {
		if err := ensureReviewHistoryDir(dir); err != nil {
			return err
		}
	}
	if source != nil {
		if hashBytes(source) != sourceHash {
			return errors.New("source snapshot does not match its hash")
		}
		if err := writeImmutableReviewFile(filepath.Join(versionDir, "source.html"), source); err != nil {
			return err
		}
	}
	revision := strings.TrimPrefix(hashBytes(review), "sha256:")
	return writeImmutableReviewFile(filepath.Join(reviewsDir, revision+".json"), review)
}

func ensureReviewHistoryDir(dir string) error {
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("review history path is not a directory: %s", dir)
	}
	return nil
}

func writeImmutableReviewFile(filename string, contents []byte) error {
	if info, err := os.Lstat(filename); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("review history path is not a regular file: %s", filename)
		}
		existing, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		if !bytes.Equal(existing, contents) {
			return fmt.Errorf("review history content differs: %s", filename)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(filename)
	temp, err := os.CreateTemp(dir, ".data-overmind-history-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(contents); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Link(temp.Name(), filename); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		existing, readErr := os.ReadFile(filename)
		if readErr != nil || !bytes.Equal(existing, contents) {
			return fmt.Errorf("review history content differs: %s", filename)
		}
	}
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func atomicWriteReview(filename string, contents []byte) (bool, error) {
	info, err := os.Lstat(filename)
	created := errors.Is(err, os.ErrNotExist)
	mode := os.FileMode(0o644)
	if err == nil {
		if !info.Mode().IsRegular() {
			return false, errors.New("review destination is not a regular file")
		}
		mode = info.Mode().Perm()
	} else if !created {
		return false, err
	}

	dir := filepath.Dir(filename)
	temp, err := os.CreateTemp(dir, ".data-overmind-review-*.tmp")
	if err != nil {
		return false, err
	}
	tempName := temp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temp.Close()
		}
		_ = os.Remove(tempName)
	}()

	if err := temp.Chmod(mode); err != nil {
		return false, err
	}
	if _, err := temp.Write(contents); err != nil {
		return false, err
	}
	if err := temp.Sync(); err != nil {
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}
	closed = true
	if err := os.Rename(tempName, filename); err != nil {
		return false, err
	}
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return created, nil
}

func hashFile(filename string) (string, error) {
	f, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func hashBytes(contents []byte) string {
	digest := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func badReviewRequest(code, message string) *reviewAPIError {
	return &reviewAPIError{Status: http.StatusBadRequest, Code: code, Message: message}
}

func internalReviewError(operation string, err error) *reviewAPIError {
	slog.Error("review API operation failed", "operation", operation, "error", err)
	return &reviewAPIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: operation + " failed"}
}

func writeReviewError(w http.ResponseWriter, apiErr *reviewAPIError) {
	if apiErr.Status == 0 {
		apiErr.Status = http.StatusInternalServerError
	}
	payload := map[string]any{
		"error": map[string]any{
			"code":    apiErr.Code,
			"message": apiErr.Message,
		},
	}
	if apiErr.Details != nil {
		payload["error"].(map[string]any)["details"] = apiErr.Details
	}
	writeReviewJSON(w, apiErr.Status, payload)
}

func writeReviewJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
