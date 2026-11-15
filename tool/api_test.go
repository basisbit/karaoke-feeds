package kfeed

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
