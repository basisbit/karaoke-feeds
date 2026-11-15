package kfeed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// itemEnv is a feed with a song pack "pop" and a theme pack "themes" (items blue-theme and red-theme), served over
// http, and a library with a theme folder.
type itemEnv struct {
	t        *testing.T
	src, pub string // feed folder and its published folder
	key, pv  string // key file and public key ("" for an unsigned feed)
	srv      *httptest.Server
	mu       sync.Mutex
	blobs    []string // blob requests in order
	lib, dir string   // library and theme folder
}

func newItemEnv(t *testing.T, signed bool) *itemEnv {
	e := &itemEnv{t: t, src: t.TempDir()}
	keys := ""
	if signed {
		e.key = filepath.Join(t.TempDir(), "feed.key")
		e.pv = newKey(t, e.key)
		keys = `"keys": ["` + e.pv + `"],`
	}
	os.WriteFile(filepath.Join(e.src, configName), []byte(`{"name":"Test Feed","operator":{"name":"Tester","contact":"mailto:takedown@example.com"},`+keys+`
		"extras": "extras", "catalogs": {"Themes": {"id": "themes", "kind": "theme"}}}`), 0o644)
	song(e.src, "Pop", "A - B", "song.txt", h("A", "B", "a.mp3"), by, "a.mp3")
	itemFolder(e.src, "Themes", "Blue", `{"id": "blue-theme", "license": {"spdx": "CC0-1.0"}}`,
		map[string]string{"Blue.ini": "[Theme]\nName=Blue\n", "Blue/bg.png": "blue png", "Blue/sub/x.png": "x png"})
	itemFolder(e.src, "Themes", "Red", `{"id": "red-theme", "license": {"spdx": "CC0-1.0"}}`,
		map[string]string{"Red.ini": "[Theme]\nName=Red\n", "Red/bg.jpg": "red jpg"})
	e.build()
	e.pub = filepath.Join(e.src, "public")
	files := feedHandler(e.pub)
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/blobs/") {
			e.mu.Lock()
			e.blobs = append(e.blobs, filepath.Base(r.URL.Path))
			e.mu.Unlock()
		}
		files.ServeHTTP(w, r)
	}))
	t.Cleanup(e.srv.Close)
	e.lib, e.dir = filepath.Join(t.TempDir(), "lib"), filepath.Join(t.TempDir(), "themes")
	return e
}

func (e *itemEnv) build() {
	e.t.Helper()
	out, err := run(e.src, e.key, true)
	if err != nil {
		e.t.Fatalf("build: %v\n%s", err, out)
	}
}

func (e *itemEnv) opts(packs ...string) Options {
	return Options{Address: e.srv.URL + "/", Key: e.pv, Library: e.lib, Packs: packs,
		Targets: map[string]Target{"theme": {Dir: e.dir, Extensions: []string{".ini", ".png", ".jpg"}}}}
}

func (e *itemEnv) sync(o Options) *Result {
	e.t.Helper()
	res, err := Sync(context.Background(), o, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

// files returns the files below the theme folder with their content.
func (e *itemEnv) files() map[string]string {
	got := map[string]string{}
	filepath.WalkDir(e.dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(e.dir, p)
			data, _ := os.ReadFile(p)
			got[filepath.ToSlash(rel)] = string(data)
		}
		return nil
	})
	return got
}

func (e *itemEnv) skipped(o Options) []string {
	e.t.Helper()
	so, _ := o.syncOpts(context.Background())
	rep, err := syncLibrary(so, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return rep.skippedPacks
}

func TestSyncItems(t *testing.T) {
	e := newItemEnv(t, true)
	o := e.opts("pop", "themes")
	if n, _, err := Plan(context.Background(), o); err != nil || n != int64(len("[Theme]\nName=Blue\n")+8+5+len("[Theme]\nName=Red\n")+7)+int64(len(strings.Repeat("dummy A - Ba.mp3", 100)))+songTxtSize(t, e) {
		t.Fatalf("Plan: %d %v", n, err)
	}
	res := e.sync(o)
	if !slices.Contains(res.Added, "blue-theme") || !slices.Contains(res.Added, "red-theme") || !slices.Contains(res.Added, "a-b") {
		t.Fatalf("added: %+v", res)
	}
	want := map[string]string{"Blue.ini": "[Theme]\nName=Blue\n", "Blue/bg.png": "blue png", "Blue/sub/x.png": "x png",
		"Red.ini": "[Theme]\nName=Red\n", "Red/bg.jpg": "red jpg"}
	if got := e.files(); !mapsEqual(got, want) {
		t.Fatalf("theme folder: %v", got)
	}
	// the top-level files were requested after all files in sub-folders
	top, sub := blobOf(t, e, "Blue.ini"), blobOf(t, e, "Blue/sub/x.png")
	if i, j := slices.Index(e.blobs, top), slices.Index(e.blobs, sub); i < 0 || j < 0 || i < j {
		t.Fatalf("order of blob requests %v: Blue.ini at %d, Blue/sub/x.png at %d", e.blobs, i, j)
	}
	if n, _, _ := Plan(context.Background(), o); n != 0 {
		t.Fatalf("Plan after Sync: %d", n)
	}
	info, err := Inspect(context.Background(), o)
	if err != nil || len(info.Packs) != 2 || info.Packs[1].Kind != "theme" || info.Packs[1].Items != 2 || info.Packs[0].Kind != KindSongs {
		t.Fatalf("Inspect: %+v %v", info, err)
	}

	// update: a local change at an item's path is replaced (SPEC 5.6 rule 5), a dropped file is deleted, a changed
	// file is updated
	os.WriteFile(filepath.Join(e.dir, "Blue", "bg.png"), []byte("my own png"), 0o644)
	itemFolder(e.src, "Themes", "Blue", "", map[string]string{"Blue/bg.png": "blue png v2", "Blue.ini": "[Theme]\nName=Blue 2\n"})
	os.RemoveAll(filepath.Join(e.src, "extras", "Themes", "Blue", "Blue", "sub"))
	e.build()
	res = e.sync(o)
	got := e.files()
	if got["Blue/bg.png"] != "blue png v2" || got["Blue.ini"] != "[Theme]\nName=Blue 2\n" || got["Blue/sub/x.png"] != "" {
		t.Fatalf("after update: %v", got)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "Blue", "sub")); err == nil {
		t.Fatal("empty folder Blue/sub left")
	}
	if !slices.Contains(res.Updated, "blue-theme") {
		t.Fatalf("update result: %+v", res)
	}

	// an item no longer listed keeps its files; an id in removed.txt is deleted
	os.RemoveAll(filepath.Join(e.src, "extras", "Themes", "Red"))
	e.build()
	e.sync(o)
	if got := e.files(); got["Red.ini"] == "" {
		t.Fatalf("delisted item lost its files: %v", got)
	}
	os.WriteFile(filepath.Join(e.src, removedName), []byte("red-theme\n"), 0o644)
	e.build()
	res = e.sync(o)
	if got := e.files(); got["Red.ini"] != "" || got["Red/bg.jpg"] != "" || !slices.Contains(res.Removed, "red-theme") {
		t.Fatalf("withdrawn item: %v %+v", got, res)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "Red")); err == nil {
		t.Fatal("empty folder Red left")
	}

	// untick the pack: files written by the launcher go, foreign and changed files stay
	os.WriteFile(filepath.Join(e.dir, "Blue", "foreign.png"), []byte("not ours"), 0o644)
	os.WriteFile(filepath.Join(e.dir, "Blue", "bg.png"), []byte("my own png"), 0o644)
	res = e.sync(e.opts("pop"))
	want = map[string]string{"Blue/bg.png": "my own png", "Blue/foreign.png": "not ours"}
	if got := e.files(); !mapsEqual(got, want) || !slices.Contains(res.Removed, "blue-theme") {
		t.Fatalf("after unticking: %v %+v", got, res)
	}
	if _, err := os.Stat(filepath.Join(e.lib, "a-b")); err != nil {
		t.Fatal("song removed together with the theme pack")
	}
}

func TestSyncItemsRefused(t *testing.T) {
	t.Run("unsigned feed", func(t *testing.T) {
		e := newItemEnv(t, false)
		if s := e.skipped(e.opts("pop", "themes")); len(s) != 1 || !strings.Contains(s[0], "needs a signed feed") {
			t.Fatalf("skipped: %q", s)
		}
		if len(e.files()) != 0 {
			t.Fatal("theme files from an unsigned feed")
		}
	})
	t.Run("key not passed", func(t *testing.T) {
		e := newItemEnv(t, true)
		o := e.opts("pop", "themes")
		o.Key = "" // trust on first use: signed, but the key did not come from the caller
		if s := e.skipped(o); len(s) != 1 || !strings.Contains(s[0], "its key") {
			t.Fatalf("skipped: %q", s)
		}
		if len(e.files()) != 0 {
			t.Fatal("theme files without the caller's key")
		}
	})
	t.Run("no target", func(t *testing.T) {
		e := newItemEnv(t, true)
		o := e.opts("pop", "themes")
		o.Targets = nil
		if s := e.skipped(o); len(s) != 1 || !strings.Contains(s[0], `no folder for kind "theme"`) {
			t.Fatalf("skipped: %q", s)
		}
		if _, err := os.Stat(filepath.Join(e.lib, "a-b")); err != nil {
			t.Fatal("songs not synced")
		}
	})
	t.Run("installed items stay when the pack is skipped", func(t *testing.T) {
		e := newItemEnv(t, true)
		e.sync(e.opts("pop", "themes"))
		o := e.opts("pop", "themes")
		o.Key = ""
		e.sync(o)
		if len(e.files()) != 5 {
			t.Fatalf("skipped pack lost its files: %v", e.files())
		}
	})
	t.Run("extension not allowed", func(t *testing.T) {
		e := newItemEnv(t, true)
		o := e.opts("pop", "themes")
		o.Targets["theme"] = Target{Dir: e.dir, Extensions: []string{".ini", ".png"}} // no .jpg: red-theme is skipped whole
		so, _ := o.syncOpts(context.Background())
		rep, err := syncLibrary(so, nil)
		if err != nil || len(rep.unsafe) != 1 || !strings.Contains(rep.unsafe[0], "red-theme") {
			t.Fatalf("%v %q", err, rep.unsafe)
		}
		if got := e.files(); got["Red.ini"] != "" || got["Blue.ini"] == "" {
			t.Fatalf("theme folder: %v", got)
		}
	})
	t.Run("same path taken over, other themes kept", func(t *testing.T) {
		e := newItemEnv(t, true)
		os.MkdirAll(filepath.Join(e.dir, "Green"), 0o755)
		os.WriteFile(filepath.Join(e.dir, "Blue.ini"), []byte("an older Blue installed by hand"), 0o644)
		os.WriteFile(filepath.Join(e.dir, "Green.ini"), []byte("someone else's theme"), 0o644)
		os.WriteFile(filepath.Join(e.dir, "Green", "bg.png"), []byte("green png"), 0o644)
		e.sync(e.opts("pop", "themes"))
		if got := e.files(); got["Blue.ini"] == "an older Blue installed by hand" || got["Blue/bg.png"] != "blue png" ||
			got["Green.ini"] != "someone else's theme" || got["Green/bg.png"] != "green png" {
			t.Fatalf("theme folder: %v", got)
		}
		e.sync(e.opts("pop"))
		want := map[string]string{"Green.ini": "someone else's theme", "Green/bg.png": "green png"}
		if got := e.files(); !mapsEqual(got, want) {
			t.Fatalf("after unticking, only the other theme may be left: %v", got)
		}
	})
	t.Run("cli flags", func(t *testing.T) {
		for _, args := range [][]string{
			{"--target", "theme=x"},
			{"--extensions", "theme=.ini"},
			{"--target", "songs=x", "--extensions", "songs=.txt"},
			{"--target", "theme=x", "--extensions", "theme=ini"},
		} {
			dirs, exts := targetsFlag{}, targetsFlag{}
			var err error
			for i := 0; i+1 < len(args) && err == nil; i += 2 {
				f := dirs
				if args[i] == "--extensions" {
					f = exts
				}
				err = f.Set(args[i+1])
			}
			if err == nil {
				_, err = cliTargets(dirs, exts)
			}
			if err == nil {
				t.Errorf("%q accepted", args)
			}
		}
		e := newItemEnv(t, true)
		code := Run([]string{"kfeed", "sync", e.srv.URL + "/", e.lib, "--all", "--key", e.pv,
			"--target", "theme=" + e.dir, "--extensions", "theme=.INI,.png,.jpg"}, "")
		if code != 0 || len(e.files()) != 5 {
			t.Fatalf("kfeed sync with --target: exit %d, files %v", code, e.files())
		}
	})
}

func blobOf(t *testing.T, e *itemEnv, path string) string {
	t.Helper()
	sum, _, err := hashFile(filepath.Join(e.dir, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func songTxtSize(t *testing.T, e *itemEnv) int64 {
	fi, err := os.Stat(filepath.Join(e.src, "songs", "Pop", "A - B", "song.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}
