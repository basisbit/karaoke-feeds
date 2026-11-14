package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestLandingPage(t *testing.T) {
	dir, keys := t.TempDir(), t.TempDir()
	k := filepath.Join(keys, "k")
	p := newKey(t, k)
	os.WriteFile(filepath.Join(dir, configName), []byte(`{"name":"Evil <script>alert(1)</script> Feed","description":"Songs & more",
		"operator":{"name":"Tester","contact":"mailto:takedown@example.com"},"homepage":"https://example.org/?a=1&b=\"><script>x</script>","keys":["`+p+`"]}`), 0o644)
	song(dir, "Pop Songs", "a", "a.txt", h("Ärzte <b>", "Lied", "a.mp3"), by, "a.mp3")
	song(dir, "Pop Songs", "b", "b.txt", h("Nobody", "Unknown", "b.mp3"), "", "b.mp3")
	rebuild(t, dir, k)
	pub := filepath.Join(dir, "public")
	page, err := os.ReadFile(filepath.Join(pub, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, want := range []string{"Evil &lt;script&gt;alert(1)&lt;/script&gt; Feed", "Songs &amp; more", "Ärzte &lt;b&gt;", "pop-songs",
		"CC-BY-4.0", "1 songs have no declared license", "mailto:takedown@example.com", strings.TrimPrefix(p, "ed25519:"), repoURL} {
		if !strings.Contains(html, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(html, "<script>alert") || strings.Contains(html, "<script>x") || strings.Contains(html, `"><script`) {
		t.Error("unescaped content from the feed on the page")
	}
	// nothing that the page's Content-Security-Policy would block, nothing loaded from elsewhere
	if regexp.MustCompile(`<style|style="|<script>|\son[a-z]+=`).MatchString(html) {
		t.Error("inline style or script on the page")
	}
	for _, m := range regexp.MustCompile(`<(link|script|img|iframe)[^>]*>`).FindAllString(html, -1) {
		if strings.Contains(m, "://") {
			t.Errorf("resource loaded from another site: %s", m)
		}
	}
	for _, f := range []string{"index.css", "index.js"} {
		data, _ := os.ReadFile(filepath.Join(pub, f))
		if len(data) == 0 || strings.Contains(string(data), "://") || strings.Contains(string(data), "@import") {
			t.Errorf("%s missing or refers to other sites", f)
		}
	}

	// kfeed serve: the page at "/", with its own policy and a short cache time
	srv := httptest.NewServer(feedHandler(pub))
	defer srv.Close()
	for path, ctype := range map[string]string{"/": "text/html", "/index.css": "text/css", "/index.js": "text/javascript"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), ctype) || len(body) == 0 ||
			resp.Header.Get("Content-Security-Policy") != pageCSP || resp.Header.Get("Cache-Control") != "public, max-age=300" {
			t.Errorf("%s: %d %q %q %q", path, resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Security-Policy"), resp.Header.Get("Cache-Control"))
		}
	}

	// a small feed lists its songs on the main page, expanded; every category also has its own song page
	if !strings.Contains(html, "<details open>") {
		t.Error("small feed: song list not shown on the main page")
	}
	songsPage, err := os.ReadFile(filepath.Join(pub, "songs-pop-songs.html"))
	if err != nil || !strings.Contains(string(songsPage), `id="filter"`) || !strings.Contains(string(songsPage), "Ärzte &lt;b&gt;") {
		t.Errorf("song page missing or incomplete: %v", err)
	}

	// a homepage that isn't a web address is refused
	os.WriteFile(filepath.Join(dir, configName), []byte(`{"name":"F","operator":{"name":"T","contact":"mailto:t@example.com"},"homepage":"javascript:alert(1)","keys":["`+p+`"]}`), 0o644)
	if _, err := run(dir, k, false); err == nil || !strings.Contains(err.Error(), "homepage") {
		t.Errorf("javascript: homepage accepted: %v", err)
	}

	// the public address from the configuration ends up on the page
	os.WriteFile(filepath.Join(dir, configName), []byte(`{"name":"Feed","operator":{"name":"T","contact":"mailto:t@example.com"},"keys":["`+p+`"],"url":"https://feed.example.org/"}`), 0o644)
	rebuild(t, dir, k)
	page, _ = os.ReadFile(filepath.Join(pub, "index.html"))
	if !strings.Contains(string(page), `data-url="https://feed.example.org/feed.json"`) {
		t.Error("configured url not on the page")
	}
}

func TestThousands(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 29999: "29,999", 1234567: "1,234,567", -1234: "-1,234"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestLandingPageLargeFeed(t *testing.T) {
	out := t.TempDir()
	lic := &License{SPDX: "CC-BY-4.0", URL: "https://creativecommons.org/licenses/by/4.0/"}
	feed := Feed{Name: "Big", Operator: Operator{Name: "x", Contact: "mailto:x@example.org"}, Generated: "2026-11-14T00:00:00Z"}
	var songs [][]Song
	for c := 0; c < 3; c++ {
		id := fmt.Sprintf("cat-%d", c)
		feed.Catalogs = append(feed.Catalogs, CatalogRef{ID: id, Names: map[string]string{"en": id}, Songs: 10000, Bytes: 5e10})
		var list []Song
		for i := 0; i < 10000; i++ {
			list = append(list, Song{ID: fmt.Sprintf("s%d-%d", c, i), Artist: fmt.Sprintf("Artist %d", i%700), Title: fmt.Sprintf("Song number %d-%d", c, i),
				Files: []File{{Role: "audio", License: lic}}})
		}
		songs = append(songs, list)
	}
	start := time.Now()
	if err := writeSite(out, "", false, feed, songs, 15e10); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	index, _ := os.ReadFile(filepath.Join(out, "index.html"))
	if len(index) > 30000 || strings.Contains(string(index), "Song number") || !strings.Contains(string(index), "songs-cat-2.html") {
		t.Errorf("main page of a 30,000-song feed: %d bytes, must stay small and only link to the song pages", len(index))
	}
	page, _ := os.ReadFile(filepath.Join(out, "songs-cat-1.html"))
	if n := strings.Count(string(page), "<li>"); n != 10000 {
		t.Errorf("song page lists %d songs, want 10000", n)
	}
	t.Logf("30,000 songs: main page %d bytes, one song page %d bytes, rendered in %v", len(index), len(page), took)

	// a category that disappears takes its page along
	feed.Catalogs, songs = feed.Catalogs[:2], songs[:2]
	if err := writeSite(out, "", false, feed, songs, 1e10); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(out, "songs-cat-2.html")) || !exists(filepath.Join(out, "songs-cat-1.html")) {
		t.Error("stale song page not removed, or a current one removed")
	}
}
