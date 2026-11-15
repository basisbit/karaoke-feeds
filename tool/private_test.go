package kfeed

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A private feed (SPEC 5.5): every subscriber has a personal address with a token in its path. The server checks the
// token and sends downloads of the big files on to a second server, as an operator with a CDN would.
func TestPrivateFeed(t *testing.T) {
	retryBase = 10 * time.Millisecond
	dir, keys := t.TempDir(), t.TempDir()
	k := filepath.Join(keys, "feed.key")
	p := newKey(t, k)
	cfg := func(extra string) {
		os.WriteFile(filepath.Join(dir, configName), []byte(`{"name":"Members Feed","operator":{"name":"T","contact":"mailto:t@example.com"},"private":true`+extra+`}`), 0o644)
	}
	song(dir, "Pop", "a", "a.txt", h("A", "One", "a.mp3"), by, "a.mp3")

	// settings: signed, and no address in feed.json, which is the same file for every subscriber
	cfg(`,"keys":[]`)
	_, err := run(dir, "", false)
	fails(t, err, "must be signed")
	cfg(`,"keys":["` + p + `"],"mirrors":["https://mirror.example.org/x/"]`)
	_, err = run(dir, k, false)
	fails(t, err, "personal address")
	cfg(`,"keys":["` + p + `"],"url":"https://feed.example.org/"`)
	_, err = run(dir, k, false)
	fails(t, err, "personal address")
	cfg(`,"keys":["` + p + `"],"moved_to":"https://new.example.org/feed.json"`)
	_, err = run(dir, k, false)
	fails(t, err, "personal address")
	cfg(`,"keys":["` + p + `"]`)
	rebuild(t, dir, k)
	pub := filepath.Join(dir, "public")
	page, _ := os.ReadFile(filepath.Join(pub, "index.html"))
	for _, want := range []string{"kfeed sync - ", "yours alone", `id="cmd-address"`, `<meta name="referrer" content="no-referrer">`} {
		if !strings.Contains(string(page), want) {
			t.Errorf("page of a private feed lacks %q", want)
		}
	}

	files := http.FileServer(http.Dir(pub))
	var referer atomic.Value
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ref := r.Header.Get("Referer"); ref != "" {
			referer.Store(ref)
		}
		if !strings.HasPrefix(r.URL.Path, "/blobs/") {
			http.Error(w, "blobs only", http.StatusForbidden)
			return
		}
		files.ServeHTTP(w, r)
	}))
	defer cdn.Close()
	const tokA, tokB = "tokenAAAAAAAAAAAAAAAAAAAAAAAAAAA", "tokenBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	var valid atomic.Value
	valid.Store(tokA)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, "/s/"+valid.Load().(string))
		if !ok || !strings.HasPrefix(rest, "/") {
			http.Error(w, "no access", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(rest, "/blobs/") {
			http.Redirect(w, r, cdn.URL+rest, http.StatusFound)
			return
		}
		r.URL.Path = rest
		files.ServeHTTP(w, r)
	}))
	defer origin.Close()
	addr := func(tok string) string { return origin.URL + "/s/" + tok + "/feed.json" }
	leaks := func(what, out string) {
		t.Helper()
		if strings.Contains(out, tokA) || strings.Contains(out, tokB) || strings.Contains(out, "/s/") {
			t.Errorf("%s shows the personal address:\n%s", what, out)
		}
	}

	// the address can be given on standard input, so it stays out of the shell history
	full := addr(tokA) + "#ed25519=" + strings.TrimPrefix(p, "ed25519:")
	stdin = strings.NewReader(full + "\n")
	defer func() { stdin = os.Stdin }()
	if got, err := readAddress(); err != nil || got != full {
		t.Fatalf("readAddress = %q, %v", got, err)
	}
	stdin = strings.NewReader("\n")
	if _, err := readAddress(); err == nil {
		t.Error("empty address accepted")
	}

	// first sync: works through the redirect; neither the output nor other users see the address
	r := syncRun{t, t.TempDir()}
	_, out, err := r.do(full, syncOpts{all: true})
	if err != nil || !strings.Contains(out, "added (1)") {
		t.Fatalf("first sync: %v\n%s", err, out)
	}
	leaks("first sync", out)
	if ref := referer.Load(); ref != nil {
		t.Errorf("the redirect target was told the personal address: Referer %v", ref)
	}
	if runtime.GOOS != "windows" {
		for name, want := range map[string]os.FileMode{"": 0o700, "state.json": 0o600, "feed.json": 0o600} {
			fi, err := os.Stat(filepath.Join(r.lib, stateDirName, name))
			if err != nil || fi.Mode().Perm() != want {
				t.Errorf("%s/%s: permissions %v, want %v (%v)", stateDirName, name, fi.Mode().Perm(), want, err)
			}
		}
	}
	txts := func() int {
		n := 0
		filepath.Walk(r.lib, func(p string, _ os.FileInfo, _ error) error {
			if strings.HasSuffix(p, ".txt") {
				n++
			}
			return nil
		})
		return n
	}
	if txts() != 1 {
		t.Fatalf("%d songs in the library, want 1", txts())
	}

	// access ended: the sync says so, and the library stays as it is
	valid.Store(tokB)
	_, out, err = r.do("", syncOpts{})
	if err != nil || !strings.Contains(out, "access refused") || txts() != 1 {
		t.Fatalf("after access ended: %v, %d songs\n%s", err, txts(), out)
	}
	leaks("sync after access ended", out)
	_, out, err = syncRun{t, t.TempDir()}.do(addr("wrongwrongwrongwrongwrongwrong"), syncOpts{all: true})
	if err == nil || !strings.Contains(err.Error(), "access refused") {
		t.Fatalf("wrong token: %v\n%s", err, out)
	}
	leaks("sync with a wrong token", out+err.Error())

	// a new personal address replaces the old one, because the feed is signed with the same key
	song(dir, "Pop", "b", "b.txt", h("B", "Two", "b.mp3"), by, "b.mp3")
	rebuild(t, dir, k)
	_, out, err = r.do(addr(tokB), syncOpts{})
	if err != nil || !strings.Contains(out, "added (1)") || txts() != 2 {
		t.Fatalf("new address: %v, %d songs\n%s", err, txts(), out)
	}
	leaks("sync with the new address", out)
	_, out, err = r.do("", syncOpts{})
	if err != nil || strings.Contains(out, "access refused") {
		t.Fatalf("the new address was not remembered: %v\n%s", err, out)
	}

	// a server that can't be reached: the error must not contain the address either
	origin.Close()
	_, out, _ = r.do("", syncOpts{})
	leaks("sync without a connection", out)

	for loc, want := range map[string]string{
		"https://feed.example.org/s/" + tokA + "/": "https://feed.example.org/…/",
		"https://feed.example.org/":                "https://feed.example.org/",
		"http://192.168.1.20:8080":                 "http://192.168.1.20:8080/",
		"/media/usb/my-feed":                       "/media/usb/my-feed",
	} {
		if got := showLoc(loc); got != want {
			t.Errorf("showLoc(%q) = %q, want %q", loc, got, want)
		}
	}
}
