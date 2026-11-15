package kfeed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
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

// apiFeed builds an unsigned feed with the catalogs pop (a, b) and rock (c) and returns its published folder.
func apiFeed(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	config(dir)
	for _, s := range [][2]string{{"Pop", "a"}, {"Pop", "b"}, {"Rock", "c"}} {
		song(dir, s[0], s[1], s[1]+".txt", h("X", s[1], s[1]+".mp3"), by, s[1]+".mp3")
	}
	rebuild(t, dir, "")
	return filepath.Join(dir, "public")
}

// quiet fails the test if f writes to standard output.
func quiet(t *testing.T, f func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	defer func() {
		os.Stdout = old
		w.Close()
		if out := <-done; out != "" {
			t.Errorf("printed to standard output: %q", out)
		}
	}()
	f()
}

// tree returns the files of a library without its state folder: path -> content.
func tree(t *testing.T, lib string) map[string]string {
	t.Helper()
	m := map[string]string{}
	filepath.WalkDir(lib, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == stateDirName {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(lib, p)
			m[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	})
	return m
}

type progressLog struct {
	mu   sync.Mutex
	list []Progress
}

func (l *progressLog) add(p Progress) { l.mu.Lock(); l.list = append(l.list, p); l.mu.Unlock() }

func (l *progressLog) phase(name string) []Progress {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Progress
	for _, p := range l.list {
		if p.Phase == name {
			out = append(out, p)
		}
	}
	return out
}

func TestAPI(t *testing.T) {
	retryBase = 10 * time.Millisecond
	srv := httptest.NewServer(feedHandler(apiFeed(t)))
	defer srv.Close()
	ctx := context.Background()
	lib := t.TempDir()
	o := Options{Address: srv.URL + "/", Library: lib}

	quiet(t, func() {
		info, err := Inspect(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		if info.Name != "Test Feed" || info.OperatorName != "Tester" || info.Signed || info.Subscribed || len(info.Packs) != 2 {
			t.Fatalf("Inspect: %+v", info)
		}
		var pop Pack
		for _, p := range info.Packs {
			if p.Chosen {
				t.Errorf("pack %s chosen before any choice", p.ID)
			}
			if p.ID == "pop" {
				pop = p
			}
		}
		if pop.Songs != 2 || pop.Bytes <= 0 {
			t.Fatalf("pack pop: %+v", pop)
		}
		if tr := tree(t, lib); len(tr) != 0 {
			t.Fatalf("Inspect wrote into the library: %v", tr)
		}

		if _, _, err := Plan(ctx, o); err != ErrNoPacks {
			t.Fatalf("Plan without a choice: %v", err)
		}
		if _, err := Sync(ctx, o, nil); err != ErrNoPacks {
			t.Fatalf("Sync without a choice: %v", err)
		}
		o.Packs = []string{"pop"}
		bytes, songs, err := Plan(ctx, o)
		if err != nil || bytes != pop.Bytes || songs != 2 {
			t.Fatalf("Plan: %d bytes, %d songs, %v; want %d bytes, 2 songs", bytes, songs, err, pop.Bytes)
		}

		var log progressLog
		res, err := Sync(ctx, o, log.add)
		if err != nil || len(res.Added) != 2 || len(res.Failed) != 0 || res.BytesDownloaded != bytes {
			t.Fatalf("Sync: %+v, %v", res, err)
		}
		if len(log.phase("checking")) == 0 || len(log.phase("finishing")) == 0 {
			t.Fatalf("phases missing: %+v", log.list)
		}
		last := log.list[len(log.list)-1]
		if last.SongsDone != 2 || last.SongsTotal != 2 || last.BytesDone != bytes || last.BytesTotal != bytes {
			t.Fatalf("last progress: %+v", last)
		}

		// the command line gets the same files
		lib2 := t.TempDir()
		syncRun{t, lib2}.ok(srv.URL+"/", syncOpts{catalogs: []string{"pop"}})
		got, want := tree(t, lib), tree(t, lib2)
		if len(got) == 0 || len(got) != len(want) {
			t.Fatalf("files differ: api %d, command line %d", len(got), len(want))
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("file %s differs", k)
			}
		}

		// the choice is stored; nothing left to do
		info, err = Inspect(ctx, Options{Library: lib})
		if err != nil || !info.Subscribed {
			t.Fatalf("after Sync: %+v %v", info, err)
		}
		for _, p := range info.Packs {
			if p.Chosen != (p.ID == "pop") {
				t.Errorf("after Sync: pack %s chosen=%v", p.ID, p.Chosen)
			}
		}
		if bytes, songs, err := Plan(ctx, Options{Library: lib}); bytes != 0 || songs != 0 || err != nil {
			t.Fatalf("Plan after Sync: %d, %d, %v", bytes, songs, err)
		}
		// adding a pack: only its songs are counted
		if _, songs, err := Plan(ctx, Options{Library: lib, Packs: []string{"pop", "rock"}}); songs != 1 || err != nil {
			t.Fatalf("Plan with rock added: %d songs, %v", songs, err)
		}
		// ... and the progress of that download starts with what is there: 2 of 3 songs, pop's bytes done
		var log2 progressLog
		if _, err := Sync(ctx, Options{Library: lib, Packs: []string{"pop", "rock"}}, log2.add); err != nil {
			t.Fatal(err)
		}
		first := log2.phase("downloading")[0]
		if first.SongsDone != 2 || first.SongsTotal != 3 || first.BytesDone != pop.Bytes || first.BytesTotal <= pop.Bytes {
			t.Fatalf("progress after adding a pack starts at %+v; pop has %d bytes", first, pop.Bytes)
		}
	})

	free, err := FreeSpace(lib)
	if err != nil || free <= 0 {
		t.Fatalf("FreeSpace: %d, %v", free, err)
	}
}

func TestAPICancel(t *testing.T) {
	retryBase = 10 * time.Millisecond
	pub := apiFeed(t)
	var stall atomic.Bool
	var ranges atomic.Int32
	stall.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" && strings.HasPrefix(r.URL.Path, "/blobs/") {
			ranges.Add(1)
		}
		if stall.Load() && strings.HasPrefix(r.URL.Path, "/blobs/") {
			data, err := os.ReadFile(filepath.Join(pub, filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/"))))
			if err == nil {
				w.WriteHeader(200)
				w.Write(data[:len(data)/2])
				w.(http.Flusher).Flush()
				<-r.Context().Done() // a stalled download, until the client gives up
				return
			}
		}
		feedHandler(pub).ServeHTTP(w, r)
	}))
	defer srv.Close()
	lib := t.TempDir()
	o := Options{Address: srv.URL + "/", Library: lib, Packs: []string{"pop", "rock"}}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(1600*time.Millisecond, cancel)
	var log progressLog
	start := time.Now()
	res, err := Sync(ctx, o, log.add)
	if !errors.Is(err, context.Canceled) || res == nil {
		t.Fatalf("cancelled Sync: %+v, %v", res, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Sync took %v to stop", d)
	}
	if n := len(log.phase("downloading")); n < 3 { // start + at least one report per second while stalled
		t.Fatalf("only %d progress reports while downloading", n)
	}
	if len(res.Added) != 0 || len(res.Failed) != 0 {
		t.Fatalf("cancelled Sync result: %+v", res)
	}
	parts := 0
	for name := range tree(t, lib) {
		if strings.HasSuffix(name, ".txt") {
			t.Fatalf("after cancel the library has %s", name)
		}
		if strings.Contains(name, partPrefix) {
			parts++ // the halves that arrived: kept to continue
		}
	}
	if parts != 3 {
		t.Fatalf("%d part files after cancel, want 3 (one per song file that was half downloaded)", parts)
	}

	stall.Store(false)
	full, _, err := Plan(context.Background(), Options{Library: lib})
	if err != nil {
		t.Fatal(err)
	}
	var log2 progressLog
	res, err = Sync(context.Background(), Options{Library: lib}, log2.add)
	if err != nil || len(res.Added) != 3 || len(res.Failed) != 0 {
		t.Fatalf("second Sync: %+v, %v", res, err)
	}
	// the halves count as done from the start, and the bar ends at 100 %
	dl := log2.phase("downloading")
	if first, last := dl[0], log2.list[len(log2.list)-1]; first.BytesDone == 0 || last.BytesDone != last.BytesTotal {
		t.Fatalf("progress with part files: first %+v, last %+v", first, last)
	}
	// the halves were continued with Range requests, not downloaded again
	if ranges.Load() < 3 || res.BytesDownloaded >= full {
		t.Fatalf("resume: %d range requests, %d bytes downloaded of %d", ranges.Load(), res.BytesDownloaded, full)
	}
	for name := range tree(t, lib) {
		if strings.Contains(name, partPrefix) {
			t.Fatalf("part file left after the download finished: %s", name)
		}
	}
	txts := 0
	for name := range tree(t, lib) {
		if strings.HasSuffix(name, ".txt") {
			txts++
		}
	}
	if txts != 3 {
		t.Fatalf("%d songs visible after the second Sync, want 3", txts)
	}
}

func TestAPIRemoveAllPacks(t *testing.T) {
	srv := httptest.NewServer(feedHandler(apiFeed(t)))
	defer srv.Close()
	ctx := context.Background()
	lib := t.TempDir()
	if _, err := Sync(ctx, Options{Address: srv.URL + "/", Library: lib, Packs: []string{"pop"}}, nil); err != nil {
		t.Fatal(err)
	}
	res, err := Sync(ctx, Options{Library: lib, Packs: []string{}}, nil)
	if err != nil || len(res.Removed) != 2 {
		t.Fatalf("no packs: %+v %v", res, err)
	}
	if n := len(tree(t, lib)); n != 0 {
		t.Fatalf("%d files left", n)
	}
	info, err := Inspect(ctx, Options{Library: lib})
	if err != nil || !info.Subscribed || slices.ContainsFunc(info.Packs, func(p Pack) bool { return p.Chosen }) {
		t.Fatalf("after removing all: %+v %v", info, err)
	}
}

func TestSignVerify(t *testing.T) {
	dir := t.TempDir()
	k1, k2 := filepath.Join(dir, "a.key"), filepath.Join(dir, "b.key")
	p1, p2 := newKey(t, k1), newKey(t, k2)
	msg := []byte(`{"app":1}`)
	sig, pub, err := Sign(k1, msg)
	if err != nil || pub != p1 {
		t.Fatalf("Sign: %q, %v (want key %q)", pub, err, p1)
	}
	if !Verify([]string{p1}, msg, sig) || !Verify([]string{p2, p1}, msg, sig+"\n") {
		t.Fatal("good signature refused")
	}
	if Verify([]string{p2}, msg, sig) {
		t.Fatal("signature accepted with the wrong key")
	}
	if Verify([]string{p1}, []byte(`{"app":2}`), sig) {
		t.Fatal("signature accepted for a changed message")
	}
	if Verify([]string{p1}, msg, "") || Verify(nil, msg, sig) {
		t.Fatal("empty signature or no key accepted")
	}
	if _, _, err := Sign(filepath.Join(dir, "missing.key"), msg); err == nil {
		t.Fatal("Sign with a missing key file")
	}
}

func TestFetch(t *testing.T) {
	retryBase = 10 * time.Millisecond
	data := make([]byte, 300<<10)
	for i := range data {
		data[i] = byte(i*7 + i>>9)
	}
	sum := sha256.Sum256(data)
	hexsum := hex.EncodeToString(sum[:])
	var cut, stall atomic.Bool
	var served atomic.Int64
	var ranges []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files/app.zip" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		mu.Unlock()
		if cut.Load() || stall.Load() {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(200)
			w.Write(data[:len(data)/2])
			served.Add(int64(len(data) / 2))
			w.(http.Flusher).Flush()
			if stall.Load() {
				<-r.Context().Done()
			}
			cut.Store(false) // the next request gets the file
			return
		}
		cw := &countingResponse{ResponseWriter: w, n: &served}
		http.ServeContent(cw, r, "app.zip", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	url := srv.URL + "/files/app.zip"
	dir := t.TempDir()
	ctx := context.Background()
	reset := func() {
		served.Store(0)
		mu.Lock()
		ranges = nil
		mu.Unlock()
		os.Remove(filepath.Join(dir, "app.zip"))
	}
	assertFile := func(name string) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%s: wrong content (%v)", name, err)
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), partPrefix) {
				t.Fatalf("part file left: %s", e.Name())
			}
		}
	}

	t.Run("good", func(t *testing.T) {
		reset()
		var last [2]int64
		calls := 0
		err := Fetch(ctx, url, "sha256:"+hexsum, int64(len(data)), filepath.Join(dir, "app.zip"), func(d, total int64) { calls++; last = [2]int64{d, total} })
		if err != nil {
			t.Fatal(err)
		}
		assertFile("app.zip")
		if calls < 2 || last != [2]int64{int64(len(data)), int64(len(data))} {
			t.Fatalf("progress: %d calls, last %v", calls, last)
		}
		if err := Fetch(ctx, url, hexsum, int64(len(data)), filepath.Join(dir, "plain.zip"), nil); err != nil {
			t.Fatal("hash without prefix, no progress:", err)
		}
		assertFile("plain.zip")
	})
	t.Run("wrong hash", func(t *testing.T) {
		reset()
		bad := strings.Repeat("0", 64)
		if err := Fetch(ctx, url, bad, int64(len(data)), filepath.Join(dir, "app.zip"), nil); err == nil || !strings.Contains(err.Error(), "hash") {
			t.Fatalf("wrong hash: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "app.zip")); err == nil {
			t.Fatal("file written despite the wrong hash")
		}
		for _, h := range []string{"", "sha256:xyz", "../../etc/passwd", strings.ToUpper(hexsum)} {
			if err := Fetch(ctx, url, h, int64(len(data)), filepath.Join(dir, "app.zip"), nil); err == nil {
				t.Fatalf("hash %q accepted", h)
			}
		}
	})
	t.Run("wrong size", func(t *testing.T) {
		reset()
		if err := Fetch(ctx, url, hexsum, int64(len(data))-1, filepath.Join(dir, "app.zip"), nil); err == nil || !strings.Contains(err.Error(), "larger") {
			t.Fatalf("smaller size: %v", err)
		}
		if err := Fetch(ctx, url, hexsum, int64(len(data))+1, filepath.Join(dir, "app.zip"), nil); err == nil || !strings.Contains(err.Error(), "ended early") {
			t.Fatalf("larger size: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "app.zip")); err == nil {
			t.Fatal("file written despite the wrong size")
		}
		os.Remove(partPath(filepath.Join(dir, "app.zip"), hexsum)) // the "ended early" attempts keep their part
	})
	t.Run("missing", func(t *testing.T) {
		reset()
		if err := Fetch(ctx, srv.URL+"/files/other.zip", hexsum, int64(len(data)), filepath.Join(dir, "app.zip"), nil); err == nil {
			t.Fatal("missing file: no error")
		}
		if err := Fetch(ctx, srv.URL+"/files/", hexsum, int64(len(data)), filepath.Join(dir, "app.zip"), nil); err == nil {
			t.Fatal("address without a file name: no error")
		}
	})
	t.Run("query and local paths", func(t *testing.T) {
		reset()
		if err := Fetch(ctx, url+"?token=a/b", hexsum, int64(len(data)), filepath.Join(dir, "q.zip"), nil); err != nil {
			t.Fatal("address with a query:", err)
		}
		src := filepath.Join(t.TempDir(), "local app.zip")
		os.WriteFile(src, data, 0o644)
		if err := Fetch(ctx, src, hexsum, int64(len(data)), filepath.Join(dir, "path.zip"), nil); err != nil {
			t.Fatal("local path:", err)
		}
		if err := Fetch(ctx, "file://"+filepath.ToSlash(src), hexsum, int64(len(data)), filepath.Join(dir, "fileurl.zip"), nil); err != nil && runtime.GOOS != "windows" {
			t.Fatal("file:// URL:", err)
		}
	})
	t.Run("resume", func(t *testing.T) {
		reset()
		cut.Store(true)
		if err := Fetch(ctx, url, hexsum, int64(len(data)), filepath.Join(dir, "app.zip"), nil); err != nil {
			t.Fatal(err)
		}
		assertFile("app.zip")
		mu.Lock()
		got := slices.Clone(ranges)
		mu.Unlock()
		want := []string{"", "bytes=" + strconv.Itoa(len(data)/2) + "-"}
		if !slices.Equal(got, want) || served.Load() != int64(len(data)) {
			t.Fatalf("requests %q (want %q), %d bytes served (want %d)", got, want, served.Load(), len(data))
		}
	})
	t.Run("cancel", func(t *testing.T) {
		reset()
		stall.Store(true)
		defer stall.Store(false)
		cctx, cancel := context.WithCancel(ctx)
		time.AfterFunc(300*time.Millisecond, cancel)
		start := time.Now()
		err := Fetch(cctx, url, hexsum, int64(len(data)), filepath.Join(dir, "app.zip"), nil)
		if !errors.Is(err, context.Canceled) || time.Since(start) > 3*time.Second {
			t.Fatalf("cancelled Fetch: %v after %v", err, time.Since(start))
		}
		fi, err := os.Stat(partPath(filepath.Join(dir, "app.zip"), hexsum))
		if err != nil || fi.Size() != int64(len(data)/2) {
			t.Fatalf("part file after cancel: %v, %v", fi, err)
		}
		stall.Store(false)
		served.Store(0)
		if err := Fetch(ctx, url, hexsum, int64(len(data)), filepath.Join(dir, "app.zip"), nil); err != nil {
			t.Fatal(err)
		}
		assertFile("app.zip")
		if served.Load() != int64(len(data)-len(data)/2) {
			t.Fatalf("after cancel %d bytes served, want only the rest", served.Load())
		}
	})
	t.Run("local", func(t *testing.T) {
		src := t.TempDir()
		os.WriteFile(filepath.Join(src, "app.zip"), data, 0o644)
		if err := Fetch(ctx, filepath.ToSlash(src)+"/app.zip", hexsum, int64(len(data)), filepath.Join(dir, "local.zip"), nil); err != nil {
			t.Fatal(err)
		}
		assertFile("local.zip")
	})
}

// countingResponse counts the body bytes written.
type countingResponse struct {
	http.ResponseWriter
	n *atomic.Int64
}

func (c *countingResponse) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.n.Add(int64(n))
	return n, err
}

func TestSplitAddress(t *testing.T) {
	sep := string(filepath.Separator)
	for _, c := range []struct{ in, dir, name string }{
		{"https://example.org/a/app.zip", "https://example.org/a/", "app.zip"},
		{"https://example.org/app.zip?t=x/y", "https://example.org/", "app.zip?t=x/y"},
		{"http://example.org/dl/app.zip#frag", "http://example.org/dl/", "app.zip#frag"},
		{"https://example.org", "", ""},
		{"https://example.org/", "", ""},
		{"https://example.org/a/?file=app.zip", "", ""},
		{filepath.Join("games", "app.zip"), "games", "app.zip"},
		{"file:///games/app.zip", filepath.FromSlash("/games"), "app.zip"},
		{"games" + sep, "", ""},
	} {
		dir, name, ok := splitAddress(c.in)
		if dir != c.dir || name != c.name || ok != (c.name != "") {
			t.Errorf("splitAddress(%q) = %q, %q, %v; want %q, %q", c.in, dir, name, ok, c.dir, c.name)
		}
	}
	if runtime.GOOS == "windows" {
		if dir, name, ok := splitAddress(`C:\games\app.zip`); !ok || dir != `C:\games` || name != "app.zip" {
			t.Errorf("Windows path: %q %q %v", dir, name, ok)
		}
	}
}
