package fileserver

import (
	"html"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var orderedItem = regexp.MustCompile(`^\s*([0-9]+)\.\s+(.+)$`)

func renderMarkdown(source []byte, baseDir string) string {
	lines := strings.Split(strings.ReplaceAll(string(source), "\r\n", "\n"), "\n")
	var out strings.Builder
	var paragraph []string
	inCode := false
	codeLanguage := ""
	listKind := ""

	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		out.WriteString("<p>")
		out.WriteString(renderInline(strings.Join(paragraph, " "), baseDir))
		out.WriteString("</p>\n")
		paragraph = paragraph[:0]
	}
	closeList := func() {
		if listKind != "" {
			out.WriteString("</" + listKind + ">\n")
			listKind = ""
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			flushParagraph()
			closeList()
			if inCode {
				out.WriteString("</code></pre>\n")
				inCode = false
				codeLanguage = ""
			} else {
				inCode = true
				codeLanguage = safeClass(strings.TrimSpace(strings.TrimPrefix(trimmed, "```")))
				out.WriteString("<pre><code")
				if codeLanguage != "" {
					out.WriteString(` class="language-` + codeLanguage + `"`)
				}
				out.WriteString(">")
			}
			continue
		}
		if inCode {
			out.WriteString(html.EscapeString(line))
			out.WriteByte('\n')
			continue
		}

		if trimmed == "" {
			flushParagraph()
			closeList()
			continue
		}
		if level, text, ok := heading(trimmed); ok {
			flushParagraph()
			closeList()
			out.WriteString("<h" + strconv.Itoa(level) + ">")
			out.WriteString(renderInline(text, baseDir))
			out.WriteString("</h" + strconv.Itoa(level) + ">\n")
			continue
		}
		if trimmed == "---" || trimmed == "***" || trimmed == "___" {
			flushParagraph()
			closeList()
			out.WriteString("<hr>\n")
			continue
		}
		if strings.HasPrefix(trimmed, "> ") {
			flushParagraph()
			closeList()
			out.WriteString("<blockquote><p>")
			out.WriteString(renderInline(strings.TrimPrefix(trimmed, "> "), baseDir))
			out.WriteString("</p></blockquote>\n")
			continue
		}
		if text, ok := unorderedText(trimmed); ok {
			flushParagraph()
			if listKind != "ul" {
				closeList()
				listKind = "ul"
				out.WriteString("<ul>\n")
			}
			out.WriteString("<li>" + renderInline(text, baseDir) + "</li>\n")
			continue
		}
		if match := orderedItem.FindStringSubmatch(trimmed); match != nil {
			flushParagraph()
			if listKind != "ol" {
				closeList()
				listKind = "ol"
				out.WriteString("<ol>\n")
			}
			out.WriteString("<li>" + renderInline(match[2], baseDir) + "</li>\n")
			continue
		}
		closeList()
		paragraph = append(paragraph, trimmed)
	}
	flushParagraph()
	closeList()
	if inCode {
		out.WriteString("</code></pre>\n")
	}
	return out.String()
}

func heading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && level < 6 && line[level] == '#' {
		level++
	}
	if level == 0 || level >= len(line) || line[level] != ' ' {
		return 0, "", false
	}
	return level, strings.TrimSpace(line[level:]), true
}

func unorderedText(line string) (string, bool) {
	if len(line) >= 2 && (line[0] == '-' || line[0] == '*' || line[0] == '+') && line[1] == ' ' {
		return strings.TrimSpace(line[2:]), true
	}
	return "", false
}

func renderInline(text, baseDir string) string {
	var out strings.Builder
	for len(text) > 0 {
		switch {
		case strings.HasPrefix(text, "!["):
			if endLabel := strings.Index(text[2:], "]("); endLabel >= 0 {
				endLabel += 2
				if endURL := strings.IndexByte(text[endLabel+2:], ')'); endURL >= 0 {
					endURL += endLabel + 2
					alt := text[2:endLabel]
					destination := markdownURL(text[endLabel+2:endURL], baseDir)
					if destination != "" {
						out.WriteString(`<img loading="lazy" alt="` + html.EscapeString(alt) + `" src="` + html.EscapeString(destination) + `">`)
						text = text[endURL+1:]
						continue
					}
				}
			}
		case strings.HasPrefix(text, "["):
			if endLabel := strings.Index(text[1:], "]("); endLabel >= 0 {
				endLabel++
				if endURL := strings.IndexByte(text[endLabel+2:], ')'); endURL >= 0 {
					endURL += endLabel + 2
					label := text[1:endLabel]
					rawDestination := text[endLabel+2 : endURL]
					destination := markdownURL(rawDestination, baseDir)
					if destination != "" {
						target := ""
						if isExternalURL(destination) || isHTML(rawDestination) {
							target = ` target="_blank" rel="noreferrer"`
						}
						out.WriteString(`<a href="` + html.EscapeString(destination) + `"` + target + `>` + html.EscapeString(label) + `</a>`)
						text = text[endURL+1:]
						continue
					}
				}
			}
		case strings.HasPrefix(text, "**"), strings.HasPrefix(text, "__"):
			marker := text[:2]
			if end := strings.Index(text[2:], marker); end >= 0 {
				end += 2
				out.WriteString("<strong>" + html.EscapeString(text[2:end]) + "</strong>")
				text = text[end+2:]
				continue
			}
		case text[0] == '`':
			if end := strings.IndexByte(text[1:], '`'); end >= 0 {
				end++
				out.WriteString("<code>" + html.EscapeString(text[1:end]) + "</code>")
				text = text[end+1:]
				continue
			}
		case text[0] == '*' || text[0] == '_':
			if end := strings.IndexByte(text[1:], text[0]); end >= 0 {
				end++
				out.WriteString("<em>" + html.EscapeString(text[1:end]) + "</em>")
				text = text[end+1:]
				continue
			}
		}

		next := nextInlineMarker(text[1:]) + 1
		if next <= 0 {
			next = len(text)
		}
		out.WriteString(html.EscapeString(text[:next]))
		text = text[next:]
	}
	return out.String()
}

func nextInlineMarker(text string) int {
	return strings.IndexFunc(text, func(r rune) bool {
		return r == '!' || r == '[' || r == '*' || r == '_' || r == '`'
	})
}

func markdownURL(value, baseDir string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	if parsed.Scheme != "" {
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "mailto":
			return parsed.String()
		default:
			return ""
		}
	}
	if strings.HasPrefix(value, "#") {
		return value
	}
	cleaned := path.Clean(path.Join("/", baseDir, parsed.Path))
	result := rawURL(strings.TrimPrefix(cleaned, "/"))
	if parsed.RawQuery != "" {
		result += "?" + parsed.RawQuery
	}
	if parsed.Fragment != "" {
		result += "#" + url.PathEscape(parsed.Fragment)
	}
	return result
}

func isExternalURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != ""
}

func safeClass(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return r
		}
		return -1
	}, value)
}
