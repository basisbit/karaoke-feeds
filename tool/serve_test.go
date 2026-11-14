package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// kfeed serve hands out the files of a feed and nothing else: no files next to them, nothing outside the folder
// (on Windows "\" is a separator too, and "C:" names a drive), no folder listings.
func TestServeOnlyFeedFiles(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "outside.txt"), []byte("SECRET-OUTSIDE"), 0o644)
	dir := filepath.Join(root, "public")
	sum := strings.Repeat("ab", 32)
	for name, data := range map[string]string{
		"feed.json": "{}", "index.html": "<html>", "index.css": "b{}", "index.js": "1", "songs-pop.html": "<html>",
		"sig/" + sum + ".sig": "s", "catalog/" + sum + ".json.gz": "c", "blobs/ab/" + sum: "BLOB",
		".secret": "SECRET-DOT", "feed.json.tmp": "SECRET-TMP", "notes.txt": "SECRET-NOTES", "blobs/ab/x.mp3": "SECRET-MP3",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(data), 0o644)
	}
	srv := httptest.NewServer(feedHandler(dir))
	defer srv.Close()
	get := func(path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+"/", nil)
		req.URL.Opaque = path // sent as given, also with %5C and ..
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	for _, path := range []string{"/", "/feed.json", "/index.html", "/index.css", "/index.js", "/songs-pop.html",
		"/sig/" + sum + ".sig", "/catalog/" + sum + ".json.gz", "/blobs/ab/" + sum} {
		if code, _ := get(path); code != 200 {
			t.Errorf("%s: %d, want 200", path, code)
		}
	}
	for _, path := range []string{
		"/..%5Coutside.txt", "/blobs/..%5C..%5Coutside.txt", "/%2E%2E%5Coutside.txt", "/..%2Foutside.txt", "/../outside.txt",
		"/.secret", "/feed.json.tmp", "/notes.txt", "/blobs/ab/x.mp3", "/blobs/", "/blobs/ab/", "/catalog/", "/sig",
		"/C:%5CWindows%5Cwin.ini", "/feed.json%00", "/FEED.JSON",
	} {
		code, body := get(path)
		if code == 200 || strings.Contains(body, "SECRET") {
			t.Errorf("%s: %d %q, must not be served", path, code, body)
		}
	}
	// the answer must not reveal whether a file outside the folder exists
	if a, _ := get("/..%5Coutside.txt"); a != 404 {
		t.Errorf("an existing file outside the folder gives %d, a missing one must give the same 404", a)
	}
	if b, _ := get("/..%5Cmissing.txt"); b != 404 {
		t.Errorf("a missing file outside the folder gives %d, want 404", b)
	}
}
