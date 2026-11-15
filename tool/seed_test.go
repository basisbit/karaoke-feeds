package kfeed

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSeeds: songs from an old installation (other folder names, other layout) are taken over instead of
// downloaded; a file of the same size but other content is not; the old folder stays unchanged.
func TestSeeds(t *testing.T) {
	dir := t.TempDir()
	config(dir)
	a := song(dir, "Pop", "a", "a.txt", h("X", "a", "a.mp3"), by, "a.mp3")
	b := song(dir, "Pop", "b", "b.txt", h("X", "bee", "b.mp3"), by, "b.mp3") // txt sizes differ from a
	rebuild(t, dir, "")
	srv := httptest.NewServer(feedHandler(filepath.Join(dir, "public")))
	defer srv.Close()

	// the old installation: song a under another name, and a file as large as b.mp3 with other content
	old := filepath.Join(t.TempDir(), "Old Karaoke", "songs")
	os.MkdirAll(filepath.Join(old, "X - a (old)"), 0o755)
	os.MkdirAll(filepath.Join(old, "Other"), 0o755)
	read := func(p string) []byte { d, _ := os.ReadFile(p); return d }
	aMP3, aTXT, bMP3, bTXT := read(filepath.Join(a, "a.mp3")), read(filepath.Join(a, "a.txt")), read(filepath.Join(b, "b.mp3")), read(filepath.Join(b, "b.txt"))
	os.WriteFile(filepath.Join(old, "X - a (old)", "song.mp3"), aMP3, 0o644)
	os.WriteFile(filepath.Join(old, "X - a (old)", "song.txt"), aTXT, 0o644)
	os.WriteFile(filepath.Join(old, "Other", "same-size.mp3"), bytes.Repeat([]byte("z"), len(bMP3)), 0o644)
	before := tree(t, old)

	ctx := context.Background()
	lib := t.TempDir()
	o := Options{Address: srv.URL + "/", Library: lib, Packs: []string{"pop"}, Seeds: []string{filepath.Dir(old)}}
	full := int64(len(aMP3) + len(aTXT) + len(bMP3) + len(bTXT))
	// Plan estimates by size: a's files and the same-size file count as there; only b.txt is left
	if n, _, err := Plan(ctx, o); err != nil || n != int64(len(bTXT)) {
		t.Fatalf("Plan with seeds: %d bytes, %v; want %d", n, err, len(bTXT))
	}
	res, err := Sync(ctx, o, nil)
	if err != nil || len(res.Added) != 2 || len(res.Failed) != 0 {
		t.Fatalf("Sync: %+v %v", res, err)
	}
	if res.BytesImported != int64(len(aMP3)+len(aTXT)) || res.BytesDownloaded != full-res.BytesImported {
		t.Fatalf("imported %d, downloaded %d; want %d and %d", res.BytesImported, res.BytesDownloaded,
			len(aMP3)+len(aTXT), full-int64(len(aMP3)+len(aTXT)))
	}
	got := tree(t, lib)
	if got["x-a/a.mp3"] != string(aMP3) || got["x-a/a.txt"] != string(aTXT) || got["x-bee/b.mp3"] != string(bMP3) {
		t.Fatalf("library: %v", keys(got))
	}
	// the media file is a hard link to the old one (no extra space), the txt a copy
	libMP3, _ := os.Stat(filepath.Join(lib, "x-a", "a.mp3"))
	oldMP3, _ := os.Stat(filepath.Join(old, "X - a (old)", "song.mp3"))
	libTXT, _ := os.Stat(filepath.Join(lib, "x-a", "a.txt"))
	oldTXT, _ := os.Stat(filepath.Join(old, "X - a (old)", "song.txt"))
	if runtime.GOOS != "windows" && !os.SameFile(libMP3, oldMP3) {
		t.Error("a.mp3 was copied, not linked")
	}
	if os.SameFile(libTXT, oldTXT) {
		t.Error("a.txt is linked; txt files must be copies")
	}
	after := tree(t, old)
	if len(after) != len(before) {
		t.Fatalf("old folder changed: %v -> %v", keys(before), keys(after))
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("old file %s changed", k)
		}
	}
	for name := range got {
		if strings.Contains(name, partPrefix) {
			t.Fatalf("leftover %s", name)
		}
	}
}

func keys(m map[string]string) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	return k
}
