package kfeed

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type syncRun struct {
	t   *testing.T
	lib string
}

func (r syncRun) do(feed string, o syncOpts) (*syncReport, string, error) {
	r.t.Helper()
	o.feed, o.lib = feed, r.lib
	if o.jobs == 0 {
		o.jobs = 3
	}
	var buf bytes.Buffer
	rep, err := syncLibrary(o, &buf)
	if rep != nil {
		printReport(&buf, rep)
	}
	return rep, buf.String(), err
}

func (r syncRun) ok(feed string, o syncOpts, wants ...string) *syncReport {
	r.t.Helper()
	rep, out, err := r.do(feed, o)
	if err != nil || len(rep.failed) > 0 {
		r.t.Fatalf("sync failed: %v\n%s", err, out)
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			r.t.Fatalf("sync output lacks %q:\n%s", w, out)
		}
	}
	return rep
}

func rebuild(t *testing.T, dir, key string) {
	t.Helper()
	out, err := run(dir, key, false)
	must(t, out, err)
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestSync(t *testing.T) {
	retryBase = 10 * time.Millisecond
	dir, keys := t.TempDir(), t.TempDir()
	k1 := filepath.Join(keys, "feed.key")
	p1 := newKey(t, k1)
	config(dir, p1)
	pub := filepath.Join(dir, "public")
	lied := h("Künstler", "Lied", "Künstler - Lied.mp3")
	lied["COVER"], lied["VIDEO"] = "cover.jpg", "video.mp4"
	song(dir, "Pop", "Lied", "Künstler - Lied.txt", lied, by, "Künstler - Lied.mp3", "cover.jpg", "video.mp4")
	song(dir, "Pop", "Lied", "Künstler - Lied [DUET].txt", h("Künstler", "Lied [DUET]", "Künstler - Lied.mp3"), "")
	song(dir, "Pop", "Undeclared", "u.txt", h("Nobody", "Unknown", "u.mp3"), "", "u.mp3")
	song(dir, "Rock", "One", "one.txt", h("One", "One", "one.mp3"), by, "one.mp3")
	take := song(dir, "Rock", "Take", "t.txt", h("Take", "Down", "t.mp3"), by, "t.mp3")
	rebuild(t, dir, k1)
	r := syncRun{t, t.TempDir()}
	url := pub + "#ed25519=" + strings.TrimPrefix(p1, "ed25519:")

	// first run: choose catalogs
	_, out, err := r.do(url, syncOpts{})
	if err != errChooseCatalogs || !strings.Contains(out, "pop") || !strings.Contains(out, "rock") {
		t.Fatalf("expected the catalog list: %v\n%s", err, out)
	}
	rep := r.ok(url, syncOpts{all: true}, "signed", "added (4)", "Not synced, no license declared (1): nobody-unknown")
	lib := r.lib
	if !exists(filepath.Join(lib, "kunstler-lied", "Künstler - Lied.txt")) || !exists(filepath.Join(lib, "kunstler-lied-duet", "Künstler - Lied.mp3")) {
		t.Fatal("songs not written")
	}
	if rep.downloaded == 0 {
		t.Fatal("nothing downloaded")
	}
	// second run: nothing to do, and without the address (stored)
	if rep = r.ok("", syncOpts{}, "unchanged: 4 songs"); rep.downloaded != 0 {
		t.Fatalf("second sync downloaded %d bytes", rep.downloaded)
	}

	// an update only fetches what changed
	f, _ := os.OpenFile(filepath.Join(dir, "songs", "Rock", "One", "one.txt"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("# timing fixed\n")
	f.Close()
	rebuild(t, dir, k1)
	if rep = r.ok("", syncOpts{}, "updated (1): one-one"); rep.downloaded > 200 {
		t.Fatalf("update downloaded %d bytes, expected only the small txt", rep.downloaded)
	}

	// a local edit is kept, unless asked
	mine := filepath.Join(lib, "one-one", "one.txt")
	os.WriteFile(mine, []byte("#TITLE:my edit\n"), 0o644)
	f, _ = os.OpenFile(filepath.Join(dir, "songs", "Rock", "One", "one.txt"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("# fixed again\n")
	f.Close()
	rebuild(t, dir, k1)
	r.ok("", syncOpts{}, "kept as they are (1): one-one/one.txt")
	if data, _ := os.ReadFile(mine); string(data) != "#TITLE:my edit\n" {
		t.Fatal("local edit overwritten")
	}
	r.ok("", syncOpts{replaceLocal: true}, "updated (1): one-one")
	if data, _ := os.ReadFile(mine); strings.Contains(string(data), "my edit") {
		t.Fatal("--replace-local did not replace")
	}

	// delisted: hidden (txt renamed, media kept); back again: shown
	lieder := filepath.Join(dir, "songs", "Pop", "Lied")
	parked := filepath.Join(dir, "parked")
	os.Rename(lieder, parked)
	rebuild(t, dir, k1)
	r.ok("", syncOpts{}, "hidden (no longer in the feed; files kept) (2)")
	if exists(filepath.Join(lib, "kunstler-lied", "Künstler - Lied.txt")) || !exists(filepath.Join(lib, "kunstler-lied", "Künstler - Lied.txt"+hiddenSuffix)) ||
		!exists(filepath.Join(lib, "kunstler-lied", "video.mp4")) {
		t.Fatal("delisted song not hidden correctly")
	}
	os.Rename(parked, lieder)
	rebuild(t, dir, k1)
	if rep = r.ok("", syncOpts{}, "shown again (2)"); rep.downloaded != 0 {
		t.Fatal("unhiding downloaded again")
	}

	// withdrawn (signed): deleted
	os.WriteFile(filepath.Join(dir, removedName), []byte("take-down\n"), 0o644)
	os.RemoveAll(take)
	rebuild(t, dir, k1)
	r.ok("", syncOpts{}, "withdrawn by the operator (deleted) (1): take-down")
	if exists(filepath.Join(lib, "take-down")) {
		t.Fatal("withdrawn song still on disk")
	}

	// renamed song: follows via previous_ids, nothing downloaded except the new txt
	song(dir, "Rock", "One", "one.txt", h("One", "One (Live)", "one.mp3"), strings.Replace(by, "{", `{"previous_ids": ["one-one"], `, 1))
	rebuild(t, dir, k1)
	r.ok("", syncOpts{replaceLocal: true}, "renamed (1): one-one → one-one-live")
	if !exists(filepath.Join(lib, "one-one-live", "one.mp3")) || exists(filepath.Join(lib, "one-one")) {
		t.Fatal("rename not applied to the folder")
	}

	// an older, correctly signed feed.json (e.g. a stale LAN copy): ignored
	feedFile := filepath.Join(pub, "feed.json")
	older, _ := os.ReadFile(feedFile)
	song(dir, "Rock", "Two", "two.txt", h("Two", "Two", "two.mp3"), by, "two.mp3")
	rebuild(t, dir, k1)
	r.ok("", syncOpts{replaceLocal: true}, "added (1): two-two")
	newer, _ := os.ReadFile(feedFile)
	os.WriteFile(feedFile, older, 0o644) // its signature is still in sig/ (kept for one build)
	r.ok("", syncOpts{}, "older than the feed this library already has")
	os.WriteFile(feedFile, newer, 0o644)

	// tampered feed.json: ignored, the library stays as it is
	good, _ := os.ReadFile(feedFile)
	os.WriteFile(feedFile, bytes.Replace(good, []byte("Test Feed"), []byte("Evil Feed"), 1), 0o644)
	r.ok("", syncOpts{}, "using the feed from the last sync", "signature missing or not valid")
	os.WriteFile(feedFile, good, 0o644)

	// unsubscribe a catalog: its songs are removed
	r.ok("", syncOpts{catalogs: []string{"pop"}}, "removed (catalog or license no longer chosen) (2): one-one-live, two-two")
	if exists(filepath.Join(lib, "one-one-live")) {
		t.Fatal("unsubscribed song still on disk")
	}

	// license choice: leave out NonCommercial songs; the songs here are CC-BY, so make one NC first
	nc := strings.Replace(by, `"CC-BY-4.0"`, `"CC-BY-NC-SA-4.0"`, 1)
	song(dir, "Pop", "Lied", "Künstler - Lied.txt", lied, nc)
	rebuild(t, dir, k1)
	ex := []string{"nc"}
	r.ok("", syncOpts{exclude: &ex}, "removed (catalog or license no longer chosen) (2): kunstler-lied, kunstler-lied-duet",
		"excluded by your license choice (2): kunstler-lied (nc: CC-BY-NC-SA-4.0)")
	if exists(filepath.Join(lib, "kunstler-lied")) {
		t.Fatal("excluded song still on disk")
	}
	r.ok("", syncOpts{}, "excluded by your license choice (2)") // the choice is remembered
	none := []string{}
	r.ok("", syncOpts{exclude: &none}, "added (2)")
	bad := []string{"xx"}
	if _, _, err := r.do("", syncOpts{exclude: &bad}); err == nil {
		t.Fatal("unknown license property accepted")
	}

	// the stored state is valid JSON and the library holds no stray temp files
	var st syncState
	data, _ := os.ReadFile(filepath.Join(lib, stateDirName, "state.json"))
	if json.Unmarshal(data, &st) != nil || len(st.Trust) != 1 {
		t.Fatal("state.json damaged or trust lost")
	}
	filepath.Walk(lib, func(p string, info os.FileInfo, err error) error {
		if strings.Contains(p, partPrefix) {
			t.Errorf("leftover temp file %s", p)
		}
		return nil
	})
}

func TestSyncHTTP(t *testing.T) {
	retryBase = 10 * time.Millisecond
	dir := t.TempDir()
	config(dir)
	for _, n := range []string{"a", "b", "c"} {
		song(dir, "Songs", n, n+".txt", h("X", n, n+".mp3"), by, n+".mp3")
	}
	rebuild(t, dir, "")
	pub := filepath.Join(dir, "public")
	var hits atomic.Int32
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1)%3 == 1 { // every third request: "slow down"
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		feedHandler(pub).ServeHTTP(w, r)
	}))
	defer busy.Close()
	r := syncRun{t, t.TempDir()}
	r.ok(busy.URL+"/feed.json", syncOpts{all: true}, "unsigned", "added (3)")

	// unsigned: another address is refused
	if _, _, err := r.do("http://127.0.0.1:1/feed.json", syncOpts{}); err == nil || !strings.Contains(err.Error(), "unsigned") {
		t.Fatalf("unsigned feed accepted from a second address: %v", err)
	}

	// a mirror is used when the main location lacks a file
	mirror := httptest.NewServer(feedHandler(pub))
	defer mirror.Close()
	os.WriteFile(filepath.Join(dir, configName), []byte(`{"name":"Test Feed","operator":{"name":"Tester","contact":"mailto:takedown@example.com"},"mirrors":["`+mirror.URL+`/"]}`), 0o644)
	song(dir, "Songs", "d", "d.txt", h("X", "d", "d.mp3"), by, "d.mp3")
	rebuild(t, dir, "")
	sum, _, _ := hashFile(filepath.Join(dir, "songs", "Songs", "d", "d.mp3"))
	half := filepath.Join(t.TempDir(), "half")
	os.MkdirAll(half, 0o755)
	os.MkdirAll(filepath.Join(half, "catalog"), 0o755) // a copy without the new blob (hard links: no admin rights needed on Windows)
	os.Link(filepath.Join(pub, "feed.json"), filepath.Join(half, "feed.json"))
	cats, _ := os.ReadDir(filepath.Join(pub, "catalog"))
	for _, c := range cats {
		os.Link(filepath.Join(pub, "catalog", c.Name()), filepath.Join(half, "catalog", c.Name()))
	}
	os.MkdirAll(filepath.Join(half, "blobs"), 0o755)
	entries, _ := os.ReadDir(filepath.Join(pub, "blobs"))
	for _, e := range entries {
		os.MkdirAll(filepath.Join(half, "blobs", e.Name()), 0o755)
		files, _ := os.ReadDir(filepath.Join(pub, "blobs", e.Name()))
		for _, f := range files {
			if f.Name() != sum {
				os.Link(filepath.Join(pub, "blobs", e.Name(), f.Name()), filepath.Join(half, "blobs", e.Name(), f.Name()))
			}
		}
	}
	r2 := syncRun{t, t.TempDir()}
	r2.ok(half, syncOpts{all: true}, "added (4)")
}

// cutter serves the feed, but ends the first n blob downloads halfway, cleanly and without any error, the way
// some CDNs have been seen to do with large files.
func cutter(pub string, n int32) (*httptest.Server, *atomic.Int32) {
	var cuts atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/blobs/") && cuts.Load() < n {
			data, err := os.ReadFile(filepath.Join(pub, filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/"))))
			if err == nil {
				cuts.Add(1)
				w.WriteHeader(200) // no Content-Length: the client can't tell the body is short
				w.Write(data[:len(data)/2])
				return
			}
		}
		feedHandler(pub).ServeHTTP(w, r)
	})), &cuts
}

func TestIncompleteDownloads(t *testing.T) {
	dir := t.TempDir()
	config(dir)
	song(dir, "Songs", "a", "a.txt", h("X", "a", "a.mp3"), by, "a.mp3")
	rebuild(t, dir, "")
	pub := filepath.Join(dir, "public")

	// two cut-offs: the retries catch them
	srv, cuts := cutter(pub, 2)
	r := syncRun{t, t.TempDir()}
	rep := r.ok(srv.URL+"/feed.json", syncOpts{all: true, jobs: 1}, "added (1)", "were repeated")
	if cuts.Load() != 2 || rep.retried != 2 {
		t.Fatalf("expected 2 cut-offs and 2 retries, got %d and %d", cuts.Load(), rep.retried)
	}
	srv.Close()

	// verify --full warns about it instead of hiding it
	srv, _ = cutter(pub, 1)
	c := &checker{}
	verify(newSource(srv.URL+"/feed.json"), "", true, c)
	if len(c.errs) > 0 || !strings.Contains(strings.Join(c.warns, " "), "correct only on attempt 2") {
		t.Fatalf("verify: errors %v, warnings %v", c.errs, c.warns)
	}
	srv.Close()

	// a server that always cuts off: the song fails after 3 attempts, and nothing broken is left behind
	srv, cuts = cutter(pub, 1000)
	defer srv.Close()
	r2 := syncRun{t, t.TempDir()}
	rep, out, _ := r2.do(srv.URL+"/feed.json", syncOpts{all: true, jobs: 1})
	if len(rep.failed) == 0 || !strings.Contains(out, "after 3 attempts") || exists(filepath.Join(r2.lib, "x-a", "a.txt")) {
		t.Fatalf("expected a failure after 3 attempts and no visible song:\n%s", out)
	}
	if cuts.Load() < 3 {
		t.Fatalf("the server cut off %d downloads, expected at least 3 attempts", cuts.Load())
	}
	c = &checker{}
	verify(newSource(srv.URL+"/feed.json"), "", true, c)
	if !strings.Contains(strings.Join(c.errs, " "), "of") {
		t.Fatalf("verify did not report the broken downloads: %v", c.errs)
	}
}

func TestFileURL(t *testing.T) {
	want := map[string]string{"file:///srv/feeds/x/feed.json": filepath.FromSlash("/srv/feeds/x/")}
	if runtime.GOOS == "windows" {
		want = map[string]string{"file:///C:/feeds/x/feed.json": `C:\feeds\x\`}
	}
	for in, out := range want {
		if got := newSource(in).base; got != out {
			t.Errorf("newSource(%q).base = %q, want %q", in, got, out)
		}
	}
}

func TestSyncHostileFeed(t *testing.T) {
	pub := t.TempDir()
	blob := []byte("x")
	sum := hashBytes(blob)
	os.MkdirAll(filepath.Join(pub, "blobs", sum[:2]), 0o755)
	os.WriteFile(filepath.Join(pub, "blobs", sum[:2], sum), blob, 0o644)
	lic := &License{SPDX: "CC0-1.0", URL: "https://creativecommons.org/publicdomain/zero/1.0/"}
	f := func(role, name string) File {
		return File{Role: role, Name: name, SHA256: "sha256:" + sum, Size: 1, License: lic}
	}
	songs := []Song{
		{ID: "a", Artist: "x", Title: "a", Files: []File{f("txt", "s.txt"), f("audio", "../../evil.mp3")}},
		{ID: "b", Artist: "x", Title: "b", Files: []File{f("txt", "s.txt"), f("audio", "CON.mp3")}},
		{ID: "c", Artist: "x", Title: "c", Files: []File{f("txt", "s.txt"), f("audio", "x.exe")}},
		{ID: "../d", Artist: "x", Title: "d", Files: []File{f("txt", "s.txt")}},
		{ID: "e", Artist: "x", Title: "e", Files: []File{f("txt", "s.txt"), {Role: "audio", Name: "big.mp3", SHA256: "sha256:" + sum, Size: 0, License: lic}}},
		{ID: "ok", Artist: "x", Title: "ok", Files: []File{f("txt", "s.txt"), f("audio", "a.mp3"), {Role: "future", Name: "../../z"}}},
	}
	cat := marshal(Catalog{Spec: Spec, ID: "h", Kind: KindSongs, Songs: songs}, false)
	cs := hashBytes(cat)
	os.MkdirAll(filepath.Join(pub, "catalog"), 0o755)
	os.WriteFile(filepath.Join(pub, "catalog", cs+".json"), cat, 0o644)
	feed := Feed{Spec: Spec, Name: "hostile", Operator: Operator{Name: "x", Contact: "mailto:x@example.org"}, Sequence: 1, Generated: "2026-11-14T00:00:00Z",
		Catalogs: []CatalogRef{{ID: "h", Kind: KindSongs, Path: "catalog/" + cs + ".json", SHA256: "sha256:" + cs, Size: int64(len(cat))}}}
	os.WriteFile(filepath.Join(pub, "feed.json"), marshal(feed, true), 0o644)
	base := t.TempDir()
	r := syncRun{t, filepath.Join(base, "lib")}
	rep, out, err := r.do(pub, syncOpts{all: true})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(rep.unsafe) != 4 || len(rep.added) != 1 || len(rep.failed) != 1 || !strings.Contains(strings.Join(rep.failed, " "), "larger than the 0 bytes") {
		t.Fatalf("unexpected result:\n%s", out)
	}
	filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if !strings.HasPrefix(p, r.lib) && p != base {
			t.Errorf("file written outside the library: %s", p)
		}
		if info != nil && !info.IsDir() && strings.Contains(p, "evil") {
			t.Errorf("hostile file written: %s", p)
		}
		return nil
	})
	if exists(filepath.Join(r.lib, "e", "s.txt")) {
		t.Error("incomplete song made visible")
	}
}

// TestResumeWithinSync: a download that breaks off is continued with a Range request on the next attempt; only the
// missing bytes are transferred again. A server without Range support (TestIncompleteDownloads) starts over.
func TestResumeWithinSync(t *testing.T) {
	retryBase = 10 * time.Millisecond
	dir := t.TempDir()
	config(dir)
	song(dir, "Songs", "a", "a.txt", h("X", "a", "a.mp3"), by, "a.mp3")
	rebuild(t, dir, "")
	pub := filepath.Join(dir, "public")
	var cut atomic.Bool
	var ranges []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/blobs/") {
			mu.Lock()
			ranges = append(ranges, r.Header.Get("Range"))
			mu.Unlock()
			data, err := os.ReadFile(filepath.Join(pub, filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/"))))
			if err == nil && len(data) > 1000 && !cut.Swap(true) { // the first download of the big file breaks off
				w.Header().Set("Content-Length", strconv.Itoa(len(data)))
				w.WriteHeader(200)
				w.Write(data[:len(data)/2])
				return // the body is shorter than announced: the client sees the connection end early
			}
		}
		feedHandler(pub).ServeHTTP(w, r)
	}))
	defer srv.Close()
	mp3, _ := os.ReadFile(filepath.Join(dir, "songs", "Songs", "a", "a.mp3"))
	txt, _ := os.ReadFile(filepath.Join(dir, "songs", "Songs", "a", "a.txt"))
	r := syncRun{t, t.TempDir()}
	rep := r.ok(srv.URL+"/feed.json", syncOpts{all: true, jobs: 1}, "added (1)")
	half := len(mp3) / 2
	if want := "bytes=" + strconv.Itoa(half) + "-"; !slices.Contains(ranges, want) {
		t.Fatalf("no Range request %q; requests: %q", want, ranges)
	}
	if rep.downloaded != int64(len(mp3)+len(txt)) || rep.retried != 1 {
		t.Fatalf("downloaded %d bytes with %d retries; want %d (each byte once) and 1", rep.downloaded, rep.retried, len(mp3)+len(txt))
	}
	filepath.Walk(r.lib, func(p string, _ os.FileInfo, _ error) error {
		if strings.Contains(p, partPrefix) {
			t.Errorf("part file left: %s", p)
		}
		return nil
	})
}

// TestKeepLocalOnRemoval: files changed here, and files kfeed did not write, survive when their song is removed
// (SPEC 5.2); an edited txt is hidden instead, so the game no longer lists the song.
func TestKeepLocalOnRemoval(t *testing.T) {
	dir := t.TempDir()
	config(dir)
	song(dir, "Pop", "P", "p.txt", h("Pop", "Song", "p.mp3"), by, "p.mp3")
	song(dir, "Rock", "R", "r.txt", h("Rock", "Song", "r.mp3"), by, "r.mp3")
	song(dir, "Rock", "T", "t.txt", h("Txt", "Edit", "t.mp3"), by, "t.mp3")
	song(dir, "Rock", "F", "f.txt", h("Foreign", "File", "f.mp3"), by, "f.mp3")
	song(dir, "Rock", "G", "g.txt", h("Guest", "Text", "g.mp3"), by, "g.mp3")
	rebuild(t, dir, "")
	pub := filepath.Join(dir, "public")
	lib := filepath.Join(t.TempDir(), "lib")
	r := syncRun{t, lib}
	in := func(parts ...string) string { return filepath.Join(append([]string{lib}, parts...)...) }
	os.MkdirAll(in("foreign-file"), 0o755)
	os.WriteFile(in("foreign-file", "f.mp3"), []byte("someone else's"), 0o644)
	os.MkdirAll(in("guest-text"), 0o755)
	os.WriteFile(in("guest-text", "g.txt"), []byte("#TITLE:someone else's txt\n"), 0o644)

	r.ok(pub, syncOpts{all: true}, "foreign-file/f.mp3")
	os.WriteFile(in("rock-song", "r.mp3"), []byte("my mix"), 0o644)
	os.WriteFile(in("txt-edit", "t.txt"), []byte("#TITLE:my timing fix\n"), 0o644)
	r.ok("", syncOpts{}, "kept as they are (4)")
	r.ok("", syncOpts{}, "kept as they are (4)") // still known as changed on the next run

	r.ok("", syncOpts{catalogs: []string{"pop"}}, "removed (catalog or license no longer chosen) (4)")
	for p, want := range map[string]string{
		in("rock-song", "r.mp3"):                 "my mix",
		in("txt-edit", "t.txt") + hiddenSuffix:   "#TITLE:my timing fix\n",
		in("foreign-file", "f.mp3"):              "someone else's",
		in("guest-text", "g.txt") + hiddenSuffix: "#TITLE:someone else's txt\n", // foreign txt: kept, but hidden
	} {
		if data, err := os.ReadFile(p); err != nil || string(data) != want {
			t.Errorf("%s: %q %v, want %q", p, data, err, want)
		}
	}
	for _, p := range []string{in("rock-song", "r.txt"), in("txt-edit", "t.txt"), in("txt-edit", "t.mp3"), in("foreign-file", "f.txt"), in("guest-text", "g.txt"), in("guest-text", "g.mp3")} {
		if exists(p) {
			t.Errorf("%s: should be deleted (unchanged, written by kfeed) or hidden", p)
		}
	}
	if !exists(in("pop-song", "p.mp3")) {
		t.Error("the chosen pack lost a song")
	}
}

// TestSyncIgnoresLocalMirrors: mirrors in a feed must be http(s) URLs. A local folder (or file://, or a Windows UNC
// path) named by the feed is never read, even when the feed's own address lacks a file.
func TestSyncIgnoresLocalMirrors(t *testing.T) {
	pub, local := t.TempDir(), t.TempDir()
	blob := []byte("x")
	sum := hashBytes(blob)
	os.MkdirAll(filepath.Join(local, "blobs", sum[:2]), 0o755) // only the "mirror" has the file
	os.WriteFile(filepath.Join(local, "blobs", sum[:2], sum), blob, 0o644)
	lic := &License{SPDX: "CC0-1.0", URL: "https://creativecommons.org/publicdomain/zero/1.0/"}
	songs := []Song{{ID: "a", Artist: "x", Title: "a", Files: []File{{Role: "txt", Name: "a.txt", SHA256: "sha256:" + sum, Size: 1, License: lic}}}}
	cat := marshal(Catalog{Spec: Spec, ID: "h", Kind: KindSongs, Songs: songs}, false)
	cs := hashBytes(cat)
	os.MkdirAll(filepath.Join(pub, "catalog"), 0o755)
	os.WriteFile(filepath.Join(pub, "catalog", cs+".json"), cat, 0o644)
	feed := Feed{Spec: Spec, Name: "m", Operator: Operator{Name: "x", Contact: "mailto:x@example.org"}, Sequence: 1, Generated: "2026-11-14T00:00:00Z",
		Mirrors:  []string{local, "file://" + filepath.ToSlash(local), `\\server\share`},
		Catalogs: []CatalogRef{{ID: "h", Kind: KindSongs, Path: "catalog/" + cs + ".json", SHA256: "sha256:" + cs, Size: int64(len(cat))}}}
	os.WriteFile(filepath.Join(pub, "feed.json"), marshal(feed, true), 0o644)
	r := syncRun{t, filepath.Join(t.TempDir(), "lib")}
	rep, out, _ := r.do(pub, syncOpts{all: true, jobs: 1})
	if rep != nil && len(rep.added) > 0 {
		t.Fatalf("a song was read from a local mirror:\n%s", out)
	}
}

// TestUnhideFailed: a song that comes back to the feed stays hidden until all its files are there. A failed
// download must not show it half-done (and a later removal must still find its hidden txt).
func TestUnhideFailed(t *testing.T) {
	retryBase = 10 * time.Millisecond
	dir := t.TempDir()
	config(dir)
	a := song(dir, "Pop", "A", "a.txt", h("Ann", "Song", "a.mp3"), by, "a.mp3")
	song(dir, "Pop", "B", "b.txt", h("Bob", "Other", "b.mp3"), by, "b.mp3") // so the catalog never gets empty
	rebuild(t, dir, "")
	pub := filepath.Join(dir, "public")
	r := syncRun{t, t.TempDir()}
	r.ok(pub, syncOpts{all: true, jobs: 1}, "added (2)")
	txt := filepath.Join(r.lib, "ann-song", "a.txt")

	parked := filepath.Join(dir, "parked")
	os.Rename(a, parked)
	rebuild(t, dir, "")
	r.ok("", syncOpts{}, "hidden (no longer in the feed; files kept) (1)")

	os.Rename(parked, a)
	mp3 := []byte("a new recording, whose download will fail")
	os.WriteFile(filepath.Join(a, "a.mp3"), mp3, 0o644)
	rebuild(t, dir, "")
	sum := hashBytes(mp3)
	if err := os.Remove(filepath.Join(pub, "blobs", sum[:2], sum)); err != nil {
		t.Fatal(err)
	}
	_, out, _ := r.do("", syncOpts{})
	if exists(txt) || !exists(txt+hiddenSuffix) {
		t.Fatalf("a song whose download failed is shown (visible txt %v):\n%s", exists(txt), out)
	}
}

// TestMovedToUnreachable: a verified feed that names a new address (moved_to) which cannot be reached is still
// used, and the unverified new address is not stored as a location.
func TestMovedToUnreachable(t *testing.T) {
	retryBase = 10 * time.Millisecond
	dir, keys := t.TempDir(), t.TempDir()
	k := filepath.Join(keys, "feed.key")
	p := newKey(t, k)
	const gone = "http://127.0.0.1:1/"
	os.WriteFile(filepath.Join(dir, configName), []byte(`{"name":"Moving Feed","operator":{"name":"T","contact":"mailto:t@example.com"},"keys":["`+p+`"],"moved_to":"`+gone+`feed.json"}`), 0o644)
	song(dir, "Pop", "a", "a.txt", h("A", "One", "a.mp3"), by, "a.mp3")
	rebuild(t, dir, k)
	r := syncRun{t, t.TempDir()}
	rep, out, err := r.do(filepath.Join(dir, "public"), syncOpts{all: true, jobs: 1})
	if err != nil || rep == nil || len(rep.added) != 1 {
		t.Fatalf("the verified feed was not used: %v\n%s", err, out)
	}
	st, err := loadState(filepath.Join(r.lib, stateDirName))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(st.Locations, gone) {
		t.Fatalf("the unreachable new address was stored: %v", st.Locations)
	}
}

// TestStaleLocationNotStored: a signed feed offered at a new place (a USB stick) that is older than the library's is
// refused, and that place is not remembered as a location of the feed.
func TestStaleLocationNotStored(t *testing.T) {
	retryBase = 10 * time.Millisecond
	dir, keys := t.TempDir(), t.TempDir()
	k := filepath.Join(keys, "feed.key")
	config(dir, newKey(t, k))
	song(dir, "Pop", "a", "a.txt", h("A", "One", "a.mp3"), by, "a.mp3")
	rebuild(t, dir, k)
	pub := filepath.Join(dir, "public")
	stale := filepath.Join(t.TempDir(), "usb")
	filepath.WalkDir(pub, func(p string, d fs.DirEntry, err error) error { // the USB copy of this version
		rel, _ := filepath.Rel(pub, p)
		if err == nil && !d.IsDir() {
			data, _ := os.ReadFile(p)
			os.MkdirAll(filepath.Dir(filepath.Join(stale, rel)), 0o755)
			os.WriteFile(filepath.Join(stale, rel), data, 0o644)
		}
		return nil
	})
	song(dir, "Pop", "b", "b.txt", h("B", "Two", "b.mp3"), by, "b.mp3")
	rebuild(t, dir, k)
	r := syncRun{t, t.TempDir()}
	r.ok(pub, syncOpts{all: true, jobs: 1}, "added (2)")
	r.do(stale, syncOpts{})
	st, err := loadState(filepath.Join(r.lib, stateDirName))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(st.Locations, stale) {
		t.Fatalf("the stale copy was stored as a location: %v", st.Locations)
	}
}
