package fileserver

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxPreviewBytes     = 1 << 20
	maxDirectoryColumns = 3
)

type fileServer struct {
	root        string
	realRoot    string
	displayRoot string
}

type pageData struct {
	RootName    string
	RootPath    string
	Selected    string
	Breadcrumbs []breadcrumbView
	Columns     []columnView
	Preview     *previewView
}

type breadcrumbView struct {
	Name    string
	Href    string
	Current bool
}

type columnView struct {
	Title   string
	Entries []entryView
}

type entryView struct {
	Name       string
	Href       string
	Target     string
	Icon       string
	Meta       string
	Kind       string
	Active     bool
	Accessible bool
}

type previewView struct {
	Name       string
	Kind       string
	URL        string
	Download   string
	HTML       string
	Meta       string
	Truncated  bool
	OpenNewTab bool
}

// New creates a read-only HTTP file browser rooted at root.
func New(root string) (http.Handler, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("absolute root: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	info, err := os.Stat(realRoot)
	if err != nil {
		return nil, fmt.Errorf("stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("root is not a directory: %s", root)
	}

	s := &fileServer{
		root:        absRoot,
		realRoot:    realRoot,
		displayRoot: filepath.Base(absRoot),
	}
	return http.HandlerFunc(s.serveHTTP), nil
}

func (s *fileServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.URL.Path == "/" {
		if download := r.URL.Query().Get("download"); download != "" {
			s.serveFile(w, r, download, true)
			return
		}
		s.serveBrowser(w, r)
		return
	}

	rel, err := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), "/"))
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	s.serveFile(w, r, rel, false)
}

func (s *fileServer) serveBrowser(w http.ResponseWriter, r *http.Request) {
	selected := cleanRelative(r.URL.Query().Get("path"))
	fullPath, err := s.resolve(selected)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	data, err := s.buildPage(selected, fullPath, info)
	if err != nil {
		slog.Error("build directory page", "path", selected, "error", err)
		http.Error(w, "cannot read directory", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'; frame-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := browserPage(data).Render(r.Context(), w); err != nil {
		slog.Error("render page", "error", err)
	}
}

func (s *fileServer) serveFile(w http.ResponseWriter, r *http.Request, rel string, download bool) {
	rel = cleanRelative(rel)
	fullPath, err := s.resolve(rel)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if info.IsDir() {
		http.Redirect(w, r, browseURL(rel), http.StatusFound)
		return
	}
	if !info.Mode().IsRegular() {
		http.Error(w, "unsupported file type", http.StatusNotFound)
		return
	}

	f, err := os.Open(fullPath)
	if err != nil {
		http.Error(w, "cannot open file", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(info.Name())))
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if download {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name()}))
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func (s *fileServer) buildPage(selected, fullPath string, selectedInfo fs.FileInfo) (pageData, error) {
	data := pageData{
		RootName: s.displayRoot,
		RootPath: s.root,
		Selected: selected,
	}

	parts := splitPath(selected)
	selectedIsDir := selectedInfo.IsDir()
	directoryDepth := len(parts)
	if !selectedIsDir && directoryDepth > 0 {
		directoryDepth--
	}
	data.Breadcrumbs = s.makeBreadcrumbs(parts[:directoryDepth])

	startDepth := max(0, directoryDepth-maxDirectoryColumns+1)
	for depth := startDepth; depth <= directoryDepth; depth++ {
		dirRel := strings.Join(parts[:depth], "/")
		dirPath, err := s.resolve(dirRel)
		if err != nil {
			return pageData{}, err
		}
		activeName := ""
		if depth < len(parts) {
			activeName = parts[depth]
		}
		column, err := s.readColumn(dirRel, dirPath, activeName)
		if err != nil {
			return pageData{}, err
		}
		data.Columns = append(data.Columns, column)
	}

	if selected != "" {
		preview, err := s.makePreview(selected, fullPath, selectedInfo)
		if err != nil {
			return pageData{}, err
		}
		data.Preview = preview
	}
	return data, nil
}

func (s *fileServer) makeBreadcrumbs(directoryParts []string) []breadcrumbView {
	crumbs := make([]breadcrumbView, 0, len(directoryParts)+1)
	crumbs = append(crumbs, breadcrumbView{
		Name:    s.displayRoot,
		Href:    browseURL(""),
		Current: len(directoryParts) == 0,
	})
	for i, name := range directoryParts {
		crumbs = append(crumbs, breadcrumbView{
			Name:    name,
			Href:    browseURL(strings.Join(directoryParts[:i+1], "/")),
			Current: i == len(directoryParts)-1,
		})
	}
	return crumbs
}

func (s *fileServer) readColumn(dirRel, dirPath, activeName string) (columnView, error) {
	items, err := os.ReadDir(dirPath)
	if err != nil {
		return columnView{}, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		ii, iErr := items[i].Info()
		ji, jErr := items[j].Info()
		if iErr == nil && jErr == nil && ii.IsDir() != ji.IsDir() {
			return ii.IsDir()
		}
		return strings.ToLower(items[i].Name()) < strings.ToLower(items[j].Name())
	})

	title := s.displayRoot
	if dirRel != "" {
		title = path.Base(dirRel)
	}
	column := columnView{Title: title}
	for _, item := range items {
		childRel := joinRelative(dirRel, item.Name())
		childPath, resolveErr := s.resolve(childRel)
		accessible := resolveErr == nil
		var info fs.FileInfo
		if accessible {
			info, err = os.Stat(childPath)
			accessible = err == nil
		}

		entry := entryView{
			Name:       item.Name(),
			Active:     item.Name() == activeName,
			Accessible: accessible,
			Kind:       "other",
			Icon:       "◇",
		}
		if !accessible {
			entry.Meta = "Unavailable"
			column.Entries = append(column.Entries, entry)
			continue
		}

		entry.Meta = formatEntryMeta(info)
		switch {
		case info.IsDir():
			entry.Kind = "directory"
			entry.Icon = "▸"
			entry.Href = browseURL(childRel)
		case isHTML(item.Name()):
			entry.Kind = "html"
			entry.Icon = "◇"
			entry.Href = rawURL(childRel)
			entry.Target = "_blank"
		case isMarkdown(item.Name()):
			entry.Kind = "markdown"
			entry.Icon = "M↓"
			entry.Href = browseURL(childRel)
		case isImage(item.Name()):
			entry.Kind = "image"
			entry.Icon = "▧"
			entry.Href = browseURL(childRel)
		default:
			entry.Href = browseURL(childRel)
		}
		column.Entries = append(column.Entries, entry)
	}
	return column, nil
}

func (s *fileServer) makePreview(rel, fullPath string, info fs.FileInfo) (*previewView, error) {
	p := &previewView{
		Name:     info.Name(),
		URL:      rawURL(rel),
		Download: downloadURL(rel),
		Meta:     formatPreviewMeta(info),
		Kind:     "unsupported",
	}
	if info.IsDir() {
		p.Kind = "directory"
		return p, nil
	}

	switch {
	case isHTML(info.Name()):
		p.Kind = "html"
		p.OpenNewTab = true
	case isMarkdown(info.Name()):
		content, truncated, err := readPreview(fullPath)
		if err != nil {
			return nil, err
		}
		p.Kind = "markdown"
		p.HTML = renderMarkdown(content, path.Dir(rel))
		p.Truncated = truncated
	case isImage(info.Name()):
		p.Kind = "image"
	case isAudio(info.Name()):
		p.Kind = "audio"
	case isVideo(info.Name()):
		p.Kind = "video"
	case strings.EqualFold(filepath.Ext(info.Name()), ".pdf"):
		p.Kind = "pdf"
	case isTextFile(fullPath, info.Name()):
		content, truncated, err := readPreview(fullPath)
		if err != nil {
			return nil, err
		}
		p.Kind = "text"
		p.HTML = string(content)
		p.Truncated = truncated
	}
	return p, nil
}

func (s *fileServer) resolve(rel string) (string, error) {
	rel = cleanRelative(rel)
	candidate := filepath.Join(s.root, filepath.FromSlash(rel))
	realPath, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	relToRoot, err := filepath.Rel(s.realRoot, realPath)
	if err != nil {
		return "", err
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) || filepath.IsAbs(relToRoot) {
		return "", errors.New("path escapes root")
	}
	return realPath, nil
}

func readPreview(filename string) ([]byte, bool, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	content, err := io.ReadAll(io.LimitReader(f, maxPreviewBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(content) > maxPreviewBytes {
		return content[:maxPreviewBytes], true, nil
	}
	return content, false, nil
}

func cleanRelative(value string) string {
	cleaned := path.Clean("/" + strings.ReplaceAll(value, "\\", "/"))
	return strings.TrimPrefix(cleaned, "/")
}

func splitPath(rel string) []string {
	if rel == "" {
		return nil
	}
	return strings.Split(rel, "/")
}

func joinRelative(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func browseURL(rel string) string {
	if rel == "" {
		return "/"
	}
	return "/?path=" + url.QueryEscape(rel)
}

func downloadURL(rel string) string {
	return "/?download=" + url.QueryEscape(rel)
}

func rawURL(rel string) string {
	parts := splitPath(rel)
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "/" + strings.Join(parts, "/")
}

func isHTML(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".html" || ext == ".htm"
}

func isMarkdown(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".markdown" || ext == ".mdown"
}

func isImage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".avif", ".gif", ".ico", ".jpeg", ".jpg", ".png", ".svg", ".webp":
		return true
	default:
		return false
	}
}

func isAudio(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".aac", ".flac", ".m4a", ".mp3", ".oga", ".ogg", ".wav":
		return true
	default:
		return false
	}
}

func isVideo(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".m4v", ".mov", ".mp4", ".ogv", ".webm":
		return true
	default:
		return false
	}
}

func isTextFile(filename, name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".c", ".conf", ".css", ".csv", ".go", ".h", ".ini", ".java", ".js", ".json", ".log", ".py", ".rs", ".sh", ".sql", ".toml", ".ts", ".tsx", ".txt", ".xml", ".yaml", ".yml":
		return true
	}
	f, err := os.Open(filename)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	return strings.HasPrefix(http.DetectContentType(buf[:n]), "text/")
}

func formatEntryMeta(info fs.FileInfo) string {
	if info.IsDir() {
		return "Folder"
	}
	return humanSize(info.Size())
}

func formatPreviewMeta(info fs.FileInfo) string {
	if info.IsDir() {
		return "Folder · Modified " + info.ModTime().Format("2006-01-02 15:04")
	}
	return humanSize(info.Size()) + " · Modified " + info.ModTime().Format("2006-01-02 15:04")
}

func humanSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}
