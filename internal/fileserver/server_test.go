package fileserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileServerBrowsingAndServing(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "hello.txt"), "hello from disk")
	mustWrite(t, filepath.Join(root, "docs", "guide.md"), "# Guide\n\nRead **this**.")
	mustWrite(t, filepath.Join(root, "site", "index.html"), "<!doctype html><title>Site</title>")

	handler, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("root listing", func(t *testing.T) {
		response := request(t, handler, "/")
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d", response.Code)
		}
		if response.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("listing is missing its content security policy")
		}
		body := response.Body.String()
		if strings.Contains(body, "{ styles }") || !strings.Contains(body, ":root {") {
			t.Fatal("page stylesheet was not rendered")
		}
		for _, want := range []string{"hello.txt", "docs", "site"} {
			if !strings.Contains(body, want) {
				t.Errorf("listing does not contain %q", want)
			}
		}
	})

	t.Run("nested markdown preview", func(t *testing.T) {
		response := request(t, handler, "/?path=docs%2Fguide.md")
		body := response.Body.String()
		if response.Code != http.StatusOK || !strings.Contains(body, "<h1>Guide</h1>") || !strings.Contains(body, "<strong>this</strong>") {
			t.Fatalf("unexpected response: status=%d body=%s", response.Code, body)
		}
	})

	t.Run("raw file", func(t *testing.T) {
		response := request(t, handler, "/hello.txt")
		if response.Code != http.StatusOK || response.Body.String() != "hello from disk" {
			t.Fatalf("unexpected response: status=%d body=%q", response.Code, response.Body.String())
		}
	})

	t.Run("download", func(t *testing.T) {
		response := request(t, handler, "/?download=hello.txt")
		if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment;") {
			t.Fatalf("unexpected download response: status=%d disposition=%q", response.Code, response.Header().Get("Content-Disposition"))
		}
	})

	t.Run("html opens raw in new tab", func(t *testing.T) {
		response := request(t, handler, "/?path=site")
		body := response.Body.String()
		if !strings.Contains(body, `href="/site/index.html"`) || !strings.Contains(body, `target="_blank"`) {
			t.Fatalf("HTML entry is not a direct new-tab link: %s", body)
		}
	})
}

func TestFileServerRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	mustWrite(t, outside, "secret")
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}

	handler, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, handler, "/outside")
	if response.Code != http.StatusNotFound {
		body, _ := io.ReadAll(response.Result().Body)
		t.Fatalf("status = %d, body = %s", response.Code, body)
	}
}

func TestFileServerLimitsColumnsAndBuildsBreadcrumbs(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "one", "two", "three", "four", "note.txt"), "deep file")

	handler, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, handler, "/?path=one%2Ftwo%2Fthree%2Ffour%2Fnote.txt")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	body := response.Body.String()
	if got := strings.Count(body, `class="column"`); got != maxDirectoryColumns {
		t.Errorf("directory columns = %d, want %d", got, maxDirectoryColumns)
	}
	for _, want := range []string{
		`href="/" class="breadcrumb"`,
		`href="/?path=one" class="breadcrumb"`,
		`href="/?path=one%2Ftwo" class="breadcrumb"`,
		`href="/?path=one%2Ftwo%2Fthree" class="breadcrumb"`,
		`href="/?path=one%2Ftwo%2Fthree%2Ffour" class="breadcrumb current" aria-current="page"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("breadcrumbs do not contain %q", want)
		}
	}
	if strings.Contains(body, ">NOTE.TXT</h2>") {
		t.Error("file name was incorrectly added as a directory breadcrumb or column")
	}
}

func request(t *testing.T, handler http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func mustWrite(t *testing.T, filename, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
