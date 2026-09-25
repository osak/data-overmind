package fileserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewContextAndSave(t *testing.T) {
	root := t.TempDir()
	sourceName := filepath.Join(root, "docs", "設計 document.html")
	mustWrite(t, sourceName, "<!doctype html><title>設計</title><article class=\"doc-body\"><p>Original paragraph.</p></article>")

	handler, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	documentURL := "/docs/%E8%A8%AD%E8%A8%88%20document.html"
	contextTarget := reviewContextPath + "?document=" + url.QueryEscape(documentURL)

	contextResponse := requestWithHeaders(t, handler, http.MethodGet, contextTarget, nil, nil)
	if contextResponse.Code != http.StatusOK {
		t.Fatalf("context status = %d, body = %s", contextResponse.Code, contextResponse.Body.String())
	}
	var context reviewContextResponse
	mustDecodeJSON(t, contextResponse, &context)
	if context.Source.Path != "docs/設計 document.html" {
		t.Errorf("source path = %q", context.Source.Path)
	}
	if context.Source.URL != documentURL {
		t.Errorf("source URL = %q, want %q", context.Source.URL, documentURL)
	}
	if context.Review.Path != "docs/設計 document.html.review.json" || context.Review.Revision != nil || context.Review.Document != nil {
		t.Errorf("unexpected empty review context: %#v", context.Review)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	quote := "Original paragraph."
	submit := reviewSubmitRequest{
		SchemaVersion: reviewSchemaVersion,
		SourceSHA256:  context.Source.SHA256,
		DocumentTitle: "  設計 document  ",
		Comments: []reviewComment{{
			ID: "comment-1",
			Block: reviewBlock{
				ID:                    "preamble::p-0",
				SectionID:             "preamble",
				ParagraphIndex:        0,
				SectionParagraphIndex: 0,
				Quote:                 quote,
				TextSHA256:            "",
			},
			Comment:   "  Add supporting evidence.  ",
			CreatedAt: now,
			UpdatedAt: now,
		}},
	}
	saveTarget := reviewSavePath + "?document=" + url.QueryEscape(documentURL)
	saveResponse := putReview(t, handler, saveTarget, submit)
	if saveResponse.Code != http.StatusCreated {
		t.Fatalf("save status = %d, body = %s", saveResponse.Code, saveResponse.Body.String())
	}
	var savedContext reviewContextResponse
	mustDecodeJSON(t, saveResponse, &savedContext)
	if savedContext.Review.Revision == nil || savedContext.Review.Document == nil {
		t.Fatalf("save response did not include persisted review: %#v", savedContext.Review)
	}
	if savedContext.Review.Document.Source.Title != "設計 document" {
		t.Errorf("saved title = %q", savedContext.Review.Document.Source.Title)
	}
	if got := savedContext.Review.Document.Comments[0].Comment; got != "Add supporting evidence." {
		t.Errorf("saved comment = %q", got)
	}
	if got := savedContext.Review.Document.Comments[0].Block.TextSHA256; got != hashBytes([]byte(quote)) {
		t.Errorf("server-computed text hash = %q", got)
	}

	reviewName := sourceName + ".review.json"
	contents, err := os.ReadFile(reviewName)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(contents, []byte("\n")) || !bytes.Contains(contents, []byte("\n  \"source\"")) {
		t.Errorf("review JSON is not stable indented output:\n%s", contents)
	}
	if strings.Contains(string(contents), `"revision"`) {
		t.Error("review file contains a self-referential revision")
	}

	contextResponse = requestWithHeaders(t, handler, http.MethodGet, contextTarget, nil, nil)
	mustDecodeJSON(t, contextResponse, &context)
	if context.Review.Revision == nil || *context.Review.Revision != *savedContext.Review.Revision || context.Review.Document == nil {
		t.Errorf("context did not reload saved review: %#v", context.Review)
	}

	submit.BaseRevision = context.Review.Revision
	submit.Comments[0].Comment = "Use a concrete benchmark."
	submit.Comments[0].UpdatedAt = time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano)
	updateResponse := putReview(t, handler, saveTarget, submit)
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updateResponse.Code, updateResponse.Body.String())
	}
	var updatedContext reviewContextResponse
	mustDecodeJSON(t, updateResponse, &updatedContext)
	submit.BaseRevision = updatedContext.Review.Revision
	submit.Comments = nil
	deleteAllResponse := putReview(t, handler, saveTarget, submit)
	if deleteAllResponse.Code != http.StatusOK {
		t.Fatalf("delete-all status = %d, body = %s", deleteAllResponse.Code, deleteAllResponse.Body.String())
	}
	var emptyContext reviewContextResponse
	mustDecodeJSON(t, deleteAllResponse, &emptyContext)
	if emptyContext.Review.Document == nil || emptyContext.Review.Document.Comments == nil || len(emptyContext.Review.Document.Comments) != 0 {
		t.Errorf("delete-all did not persist an empty comment array: %#v", emptyContext.Review.Document)
	}
}

func TestReviewSaveDetectsSourceAndReviewConflicts(t *testing.T) {
	root := t.TempDir()
	sourceName := filepath.Join(root, "report.html")
	mustWrite(t, sourceName, "<!doctype html><p>First version</p>")
	handler, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	contextTarget := reviewContextPath + "?document=%2Freport.html"
	contextResponse := requestWithHeaders(t, handler, http.MethodGet, contextTarget, nil, nil)
	var context reviewContextResponse
	mustDecodeJSON(t, contextResponse, &context)
	submit := validReviewSubmit(context.Source.SHA256, "First version")
	saveTarget := reviewSavePath + "?document=%2Freport.html"
	created := putReview(t, handler, saveTarget, submit)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var createdContext reviewContextResponse
	mustDecodeJSON(t, created, &createdContext)

	t.Run("review revision conflict", func(t *testing.T) {
		wrong := "sha256:" + strings.Repeat("0", 64)
		submit.BaseRevision = &wrong
		response := putReview(t, handler, saveTarget, submit)
		if response.Code != http.StatusConflict || reviewErrorCode(t, response) != "review_changed" {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("source conflict", func(t *testing.T) {
		mustWrite(t, sourceName, "<!doctype html><p>Second version</p>")
		submit.BaseRevision = createdContext.Review.Revision
		response := putReview(t, handler, saveTarget, submit)
		if response.Code != http.StatusConflict || reviewErrorCode(t, response) != "source_changed" {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})
}

func TestReviewAPIRejectsUnsafeRequests(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "report.html"), "<!doctype html><p>Review me</p>")
	mustWrite(t, filepath.Join(root, "plain.txt"), "not html")
	handler, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	contextResponse := requestWithHeaders(t, handler, http.MethodGet, reviewContextPath+"?document=%2Freport.html", nil, nil)
	var context reviewContextResponse
	mustDecodeJSON(t, contextResponse, &context)
	payload, err := json.Marshal(validReviewSubmit(context.Source.SHA256, "Review me"))
	if err != nil {
		t.Fatal(err)
	}
	target := reviewSavePath + "?document=%2Freport.html"

	tests := []struct {
		name    string
		method  string
		target  string
		headers map[string]string
		body    []byte
		status  int
		code    string
	}{
		{name: "wrong method", method: http.MethodPost, target: target, status: http.StatusMethodNotAllowed, code: "method_not_allowed"},
		{name: "missing content type", method: http.MethodPut, target: target, headers: map[string]string{"X-Data-Overmind-Review": "1"}, body: payload, status: http.StatusUnsupportedMediaType, code: "unsupported_media_type"},
		{name: "missing custom header", method: http.MethodPut, target: target, headers: map[string]string{"Content-Type": "application/json"}, body: payload, status: http.StatusForbidden, code: "missing_review_header"},
		{name: "cross origin", method: http.MethodPut, target: target, headers: reviewHeaders("https://attacker.example"), body: payload, status: http.StatusForbidden, code: "cross_origin"},
		{name: "unnormalized document", method: http.MethodGet, target: reviewContextPath + "?document=%2Fdocs%2F..%2Freport.html", status: http.StatusBadRequest, code: "invalid_document"},
		{name: "non HTML source", method: http.MethodGet, target: reviewContextPath + "?document=%2Fplain.txt", status: http.StatusNotFound, code: "source_not_found"},
		{name: "unknown endpoint", method: http.MethodGet, target: reviewAPIPrefix + "unknown", status: http.StatusNotFound, code: "not_found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := requestWithHeaders(t, handler, test.method, test.target, test.body, test.headers)
			if response.Code != test.status || reviewErrorCode(t, response) != test.code {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}

	t.Run("unknown JSON field", func(t *testing.T) {
		body := append(payload[:len(payload)-1], []byte(`,"unexpected":true}`)...)
		response := requestWithHeaders(t, handler, http.MethodPut, target, body, reviewHeaders("http://example.com"))
		if response.Code != http.StatusBadRequest || reviewErrorCode(t, response) != "invalid_json" {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("root escaping symlink", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "outside.html")
		mustWrite(t, outside, "<!doctype html><p>outside</p>")
		if err := os.Symlink(outside, filepath.Join(root, "outside.html")); err != nil {
			t.Fatal(err)
		}
		response := requestWithHeaders(t, handler, http.MethodGet, reviewContextPath+"?document=%2Foutside.html", nil, nil)
		if response.Code != http.StatusNotFound || reviewErrorCode(t, response) != "source_not_found" {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})
}

func TestReviewAPILeavesInvalidExistingReviewUntouched(t *testing.T) {
	root := t.TempDir()
	sourceName := filepath.Join(root, "report.html")
	reviewName := sourceName + ".review.json"
	mustWrite(t, sourceName, "<!doctype html><p>Review me</p>")
	mustWrite(t, reviewName, "this is not JSON\n")
	handler, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	response := requestWithHeaders(t, handler, http.MethodGet, reviewContextPath+"?document=%2Freport.html", nil, nil)
	if response.Code != http.StatusConflict || reviewErrorCode(t, response) != "invalid_existing_review" {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	contents, err := os.ReadFile(reviewName)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "this is not JSON\n" {
		t.Errorf("invalid review was modified: %q", contents)
	}
}

func TestReviewSaveSerializesConcurrentCreates(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "report.html"), "<!doctype html><p>Review me</p>")
	handler, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	contextResponse := requestWithHeaders(t, handler, http.MethodGet, reviewContextPath+"?document=%2Freport.html", nil, nil)
	var context reviewContextResponse
	mustDecodeJSON(t, contextResponse, &context)
	submit := validReviewSubmit(context.Source.SHA256, "Review me")
	body, err := json.Marshal(submit)
	if err != nil {
		t.Fatal(err)
	}
	target := reviewSavePath + "?document=%2Freport.html"

	start := make(chan struct{})
	statuses := make(chan int, 2)
	for range 2 {
		go func() {
			<-start
			request := httptest.NewRequest(http.MethodPut, "http://example.com"+target, bytes.NewReader(body))
			for name, value := range reviewHeaders("http://example.com") {
				request.Header.Set(name, value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			statuses <- response.Code
		}()
	}
	close(start)
	first, second := <-statuses, <-statuses
	if !((first == http.StatusCreated && second == http.StatusConflict) || (first == http.StatusConflict && second == http.StatusCreated)) {
		t.Fatalf("concurrent statuses = %d, %d; want one 201 and one 409", first, second)
	}
}

func validReviewSubmit(sourceHash, quote string) reviewSubmitRequest {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return reviewSubmitRequest{
		SchemaVersion: reviewSchemaVersion,
		SourceSHA256:  sourceHash,
		DocumentTitle: "Report",
		Comments: []reviewComment{{
			ID: "comment-1",
			Block: reviewBlock{
				ID:                    "preamble::p-0",
				SectionID:             "preamble",
				ParagraphIndex:        0,
				SectionParagraphIndex: 0,
				Quote:                 quote,
				TextSHA256:            hashBytes([]byte(quote)),
			},
			Comment:   "Please clarify this paragraph.",
			CreatedAt: now,
			UpdatedAt: now,
		}},
	}
}

func putReview(t *testing.T, handler http.Handler, target string, request reviewSubmitRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return requestWithHeaders(t, handler, http.MethodPut, target, body, reviewHeaders("http://example.com"))
}

func reviewHeaders(origin string) map[string]string {
	return map[string]string{
		"Content-Type":           "application/json",
		"X-Data-Overmind-Review": "1",
		"Origin":                 origin,
	}
}

func requestWithHeaders(t *testing.T, handler http.Handler, method, target string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://example.com"+target, bytes.NewReader(body))
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func mustDecodeJSON(t *testing.T, response *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), destination); err != nil {
		t.Fatalf("decode JSON response: %v\nbody: %s", err, response.Body.String())
	}
}

func reviewErrorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response: %v\nbody: %s", err, response.Body.String())
	}
	if payload.Error.Code == "" {
		t.Fatalf("response does not contain an error code: %s", response.Body.String())
	}
	return payload.Error.Code
}

func TestReviewBodyLimit(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "report.html"), "<!doctype html><p>Review me</p>")
	handler, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(fmt.Sprintf(`{"padding":%q}`, strings.Repeat("x", maxReviewBodyBytes)))
	response := requestWithHeaders(t, handler, http.MethodPut, reviewSavePath+"?document=%2Freport.html", body, reviewHeaders("http://example.com"))
	if response.Code != http.StatusRequestEntityTooLarge || reviewErrorCode(t, response) != "request_too_large" {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}
