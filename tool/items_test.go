package kfeed

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func itemWith(paths ...string) Item {
	lic := &License{SPDX: "CC0-1.0", URL: "https://creativecommons.org/publicdomain/zero/1.0/"}
	it := Item{ID: "blue-theme", Names: map[string]string{"en": "Blue"}}
	for _, p := range paths {
		it.Files = append(it.Files, ItemFile{Path: p, SHA256: "sha256:" + strings.Repeat("a", 64), Size: 10, License: lic})
	}
	return it
}

func TestItemProblem(t *testing.T) {
	exts := []string{".ini", ".png", ".jpg"}
	good := itemWith("Blue.ini", "Blue/background.png", "Blue/fonts/a.png", "a/b/c/d/e/f/g/h.png")
	if p := itemProblem(good, exts); p != "" {
		t.Fatalf("good item refused: %s", p)
	}
	if p := itemProblem(itemWith("Blue.ini", "x.ttf"), nil); p != "" {
		t.Fatalf("without an extension list only the block list counts: %s", p)
	}
	long := strings.Repeat("a", 150) + "/" + strings.Repeat("b", 150) + "/" + strings.Repeat("c", 100) + ".png"
	cases := []struct {
		name string
		it   Item
		want string
	}{
		{"bad id", Item{ID: "Blue Theme", Files: good.Files}, "invalid id"},
		{"no files", Item{ID: "x"}, "no files"},
		{"empty path", itemWith(""), "1 to 160 bytes"},
		{"empty part", itemWith("Blue//a.png"), "1 to 160 bytes"},
		{"absolute", itemWith("/etc/a.png"), "1 to 160 bytes"},
		{"dot dot", itemWith("../a.png"), "starts with a dot"},
		{"dot part", itemWith("./a.png"), "starts with a dot"},
		{"hidden file", itemWith(".a.png"), "starts with a dot"},
		{"backslash", itemWith(`a\b.png`), `contains one of`},
		{"colon", itemWith("C:/a.png"), `contains one of`},
		{"star", itemWith("a*.png"), `contains one of`},
		{"control", itemWith("a\tb.png"), "control character"},
		{"trailing space", itemWith("Blue /a.png"), "ends with a dot or a space"},
		{"trailing dot folder", itemWith("Blue./a.png"), "ends with a dot or a space"},
		{"device folder", itemWith("con/a.png"), "device name"},
		{"device file", itemWith("Blue/LPT1.png"), "device name"},
		{"part too long", itemWith(strings.Repeat("a", 161) + ".png"), "1 to 160 bytes"},
		{"9 parts", itemWith("a/b/c/d/e/f/g/h/i.png"), "8 parts"},
		{"401 bytes", itemWith(long), "400 bytes"},
		{"not in list", itemWith("Blue.ini", "a.gif"), `".gif" is not allowed`},
		{"no extension", itemWith("README"), `"" is not allowed`},
		{"blocked exe", itemWith("setup.exe"), `".exe" is not allowed`},
		{"blocked upper case", itemWith("Run.BAT"), `".bat" is not allowed`},
		{"blocked desktop", itemWith("a/x.desktop"), `".desktop" is not allowed`},
		{"case twice", itemWith("Blue.ini", "blue.INI"), "twice"},
		{"file and folder", itemWith("Blue.png", "Blue.png/x.png"), "file and also a folder"},
		{"no license", Item{ID: "x", Files: []ItemFile{{Path: "a.png", SHA256: good.Files[0].SHA256, Size: 1}}}, "no declared license"},
		{"NOLICENSE", Item{ID: "x", Files: []ItemFile{{Path: "a.png", SHA256: good.Files[0].SHA256, Size: 1, License: &License{SPDX: "NOLICENSE"}}}}, "no declared license"},
		{"license without url", Item{ID: "x", Files: []ItemFile{{Path: "a.png", SHA256: good.Files[0].SHA256, Size: 1, License: &License{SPDX: "MIT"}}}}, "no declared license"},
		{"bad hash", Item{ID: "x", Files: []ItemFile{{Path: "a.png", SHA256: "sha256:xyz", Size: 1, License: good.Files[0].License}}}, "malformed"},
		{"negative size", Item{ID: "x", Files: []ItemFile{{Path: "a.png", SHA256: good.Files[0].SHA256, Size: -1, License: good.Files[0].License}}}, "malformed"},
	}
	for _, tc := range cases {
		exts := exts
		if strings.HasPrefix(tc.name, "blocked") {
			exts = append(exts, ".exe", ".bat", ".desktop") // the block list wins over the client's list
		}
		if p := itemProblem(tc.it, exts); !strings.Contains(p, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.name, p, tc.want)
		}
	}
}

func TestDecodeCatalogKind(t *testing.T) {
	data := []byte(`{"spec":"` + Spec + `","id":"themes","kind":"theme","items":[]}`)
	ref := CatalogRef{ID: "themes", Kind: "theme", Path: "catalog/x.json", SHA256: "sha256:" + hashBytes(data), Size: int64(len(data))}
	if _, err := decodeCatalog(ref, data); err != nil {
		t.Fatal(err)
	}
	ref.Kind = "songs"
	if _, err := decodeCatalog(ref, data); err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("kind mismatch accepted: %v", err)
	}
}

// itemFolder writes an item folder below <feed>/extras/<pack>/<item> with files (path -> content) and a meta file.
func itemFolder(feed, pack, item, meta string, files map[string]string) {
	dir := filepath.Join(feed, "extras", pack, item)
	for p, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, filepath.FromSlash(p))), 0o755)
		os.WriteFile(filepath.Join(dir, filepath.FromSlash(p)), []byte(body), 0o644)
	}
	if meta != "" {
		os.WriteFile(filepath.Join(dir, metaName), []byte(meta), 0o644)
	}
}

const cc0 = `{"license": {"spdx": "CC0-1.0"}}`

// itemFeed is a feed folder with one song catalog and one theme pack.
func itemFeed(t *testing.T) string {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, configName), []byte(`{"name":"Test Feed","operator":{"name":"Tester","contact":"mailto:takedown@example.com"},
		"extras": "extras", "catalogs": {"Themes": {"id": "themes", "kind": "theme", "names": {"de": "Designs"}}}}`), 0o644)
	song(dir, "Pop", "A - B", "song.txt", h("A", "B", "a.mp3"), by, "a.mp3")
	itemFolder(dir, "Themes", "Blue", `{"id": "blue-theme", "names": {"de": "Blau"}, "license": {"spdx": "CC0-1.0"}}`,
		map[string]string{"Blue.ini": "[Theme]\n", "Blue/bg.png": "png", "Blue/sub/x.png": "x", ".DS_Store": "junk", "Blue/.hidden/y.png": "y"})
	itemFolder(dir, "Themes", "Red Theme", cc0, map[string]string{"Red.ini": "[Theme]\n"})
	itemFolder(dir, "Themes", "No Meta", "", map[string]string{"x.ini": "x"})
	itemFolder(dir, "Themes", "No License", `{"license": {"spdx": "NOLICENSE"}}`, map[string]string{"x.ini": "x"})
	itemFolder(dir, "Themes", "Program", cc0, map[string]string{"x.ini": "x", "run.exe": "MZ"})
	itemFolder(dir, "Themes", "Same Id", `{"id": "a-b", "license": {"spdx": "CC0-1.0"}}`, map[string]string{"x.ini": "x"})
	return dir
}

func TestBuildItems(t *testing.T) {
	dir := itemFeed(t)
	out, err := run(dir, "", false)
	must(t, out, err, "1 songs", "2 items", "(kind theme)", "No Meta: needs a feed-meta.json", "No License: feed-meta.json needs",
		`Program: path "run.exe": extension ".exe" is not allowed`, `the id "a-b" is already used`)
	pub := filepath.Join(dir, "public")
	if p := problems(newSource(pub), "", true); p != "" {
		t.Fatalf("verify: %s", p)
	}
	var feed Feed
	data, _ := os.ReadFile(filepath.Join(pub, "feed.json"))
	json.Unmarshal(data, &feed)
	if len(feed.Catalogs) != 2 || feed.Catalogs[0].Kind != KindSongs || feed.Catalogs[0].Songs != 1 {
		t.Fatalf("song catalog: %+v", feed.Catalogs)
	}
	ref := feed.Catalogs[1]
	if ref.ID != "themes" || ref.Kind != "theme" || ref.Items != 2 || ref.Songs != 0 || ref.Names["de"] != "Designs" || ref.Bytes != int64(len("[Theme]\n")+3+1) {
		t.Fatalf("theme catalog reference: %+v", ref) // Blue.ini and Red.ini have the same bytes: counted once
	}
	stored, _ := os.ReadFile(filepath.Join(pub, filepath.FromSlash(ref.Path)))
	cat, err := decodeCatalog(ref, stored)
	if err != nil {
		t.Fatal(err)
	}
	if cat.Kind != "theme" || len(cat.Songs) != 0 || len(cat.Items) != 2 {
		t.Fatalf("catalog: %+v", cat)
	}
	blue, red := cat.Items[0], cat.Items[1]
	var paths []string
	for _, f := range blue.Files {
		paths = append(paths, f.Path)
		if f.License == nil || f.License.SPDX != "CC0-1.0" || f.License.URL == "" {
			t.Errorf("%s: license %+v", f.Path, f.License)
		}
		sum := strings.TrimPrefix(f.SHA256, "sha256:")
		if _, err := os.Stat(filepath.Join(pub, "blobs", sum[:2], sum)); err != nil {
			t.Errorf("%s: blob missing", f.Path)
		}
	}
	if blue.ID != "blue-theme" || blue.Names["de"] != "Blau" || blue.Names["en"] != "Blue" || strings.Join(paths, " ") != "Blue.ini Blue/bg.png Blue/sub/x.png" {
		t.Fatalf("blue item: %+v (hidden files and feed-meta.json must not be part of it)", blue)
	}
	if red.ID != "red-theme" || red.Names["en"] != "Red Theme" {
		t.Fatalf("red item: %+v", red)
	}
	page, _ := os.ReadFile(filepath.Join(pub, "index.html"))
	if !strings.Contains(string(page), "kind <code>theme</code> · 2 items") || exists(filepath.Join(pub, "songs-themes.html")) || !exists(filepath.Join(pub, "songs-pop.html")) {
		t.Fatalf("feed page: item pack not listed, or a song page made for it")
	}
	// unchanged rebuild; then an item changes and one is withdrawn
	must(t, out, err)
	out, err = run(dir, "", false)
	must(t, out, err, "No changes")
	os.WriteFile(filepath.Join(dir, "extras", "Themes", "Blue", "Blue.ini"), []byte("[Theme]\nName=Blue\n"), 0o644)
	os.RemoveAll(filepath.Join(dir, "extras", "Themes", "Red Theme"))
	os.WriteFile(filepath.Join(dir, removedName), []byte("red-theme\n"), 0o644)
	out, err = run(dir, "", false)
	must(t, out, err, "changed (1): blue-theme", "withdrawn", "red-theme")
}

func TestBuildItemsConfig(t *testing.T) {
	dir := itemFeed(t)
	cfg := func(catalogs string) {
		os.WriteFile(filepath.Join(dir, configName), []byte(`{"name":"Test Feed","operator":{"name":"Tester","contact":"mailto:takedown@example.com"},
			"extras": "extras", "catalogs": `+catalogs+`}`), 0o644)
	}
	cfg(`{}`)
	_, err := run(dir, "", false)
	fails(t, err, `pack folder "Themes" in extras needs a kind`)
	cfg(`{"Themes": {"kind": "songs"}}`)
	_, err = run(dir, "", false)
	fails(t, err, `needs a kind`)
	cfg(`{"Themes": {"kind": "Theme Pack"}}`)
	_, err = run(dir, "", false)
	fails(t, err, `needs a kind`)
	cfg(`{"Themes": {"kind": "theme", "id": "pop"}}`)
	_, err = run(dir, "", false)
	fails(t, err, `"pop" is used twice`)
	cfg(`{"Themes": {"kind": "theme"}, "Pop": {"kind": "theme"}}`)
	_, err = run(dir, "", false)
	fails(t, err, `packs of other kinds go into the "extras" folder`)
}
