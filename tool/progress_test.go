package kfeed

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestProgressWithoutRange: the bytes of a part file count as done when a download is continued; when the server
// ignores the Range request and sends the whole file, they must not be counted a second time.
func TestProgressWithoutRange(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 1000)
	sum := hashBytes(data)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(data) // no Range support: always 200 with the whole file
	}))
	defer srv.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, "a.mp3")
	part := partPath(dest, sum)
	half := int64(len(data) / 2)
	if err := os.WriteFile(part, data[:half], 0o644); err != nil {
		t.Fatal(err)
	}
	size := int64(len(data))
	tr := &tracker{}
	tr.p.BytesTotal, tr.p.BytesDone = 10*size, half // other files follow; the part counts as done (Sync's estimate)
	s := newSource(srv.URL)
	if _, _, err := fetchOnce(s, "blobs/"+sum[:2]+"/"+sum, sum, size, dest, part, tr); err != nil {
		t.Fatal(err)
	}
	if tr.p.BytesDone != size {
		t.Fatalf("done %d bytes after one file of %d", tr.p.BytesDone, size)
	}
}

// TestFetchProgressOneAtATime: Fetch calls its progress callback from one goroutine at a time (the launcher updates
// its window from it). Here the 500 ms tick is still in a slow callback when the download ends.
func TestFetchProgressOneAtATime(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Write(data[:500])
		w.(http.Flusher).Flush()
		time.Sleep(700 * time.Millisecond) // the tick at 500 ms comes during the download
		w.Write(data[500:])
	}))
	defer srv.Close()
	var inside, overlap atomic.Int32
	err := Fetch(context.Background(), srv.URL+"/a.bin", "sha256:"+hashBytes(data), 1000, filepath.Join(t.TempDir(), "a.bin"),
		func(done, total int64) {
			if inside.Add(1) > 1 {
				overlap.Store(1)
			}
			time.Sleep(400 * time.Millisecond)
			inside.Add(-1)
		})
	if err != nil {
		t.Fatal(err)
	}
	if overlap.Load() != 0 {
		t.Fatal("the progress callback ran twice at the same time")
	}
}
