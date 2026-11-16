package kfeed

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
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

