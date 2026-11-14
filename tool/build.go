package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	configName  = "feed-config.json"
	metaName    = "feed-meta.json"
	removedName = "removed.txt"
)

// Config is feed-config.json in the feed folder.
type Config struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Homepage    string                `json:"homepage"`
	Operator    Operator              `json:"operator"`
	Songs       string                `json:"songs"`
	Output      string                `json:"output"`
	Keys        []string              `json:"keys"`
	Mirrors     []string              `json:"mirrors"`
	MovedTo     string                `json:"moved_to"`
	URL         string                `json:"url"`     // public address of feed.json, shown on the landing page
	Private     bool                  `json:"private"` // every subscriber gets a personal address (SPEC 5.5)
	Catalogs    map[string]CatalogCfg `json:"catalogs"`
}

// CatalogCfg optionally overrides id and display names of a catalog folder.
type CatalogCfg struct {
	ID    string            `json:"id"`
	Names map[string]string `json:"names"`
}

// Meta is the optional feed-meta.json next to a song's txt file.
type Meta struct {
	ID          string             `json:"id"`
	IDs         map[string]string  `json:"ids"`
	PreviousIDs []string           `json:"previous_ids"`
	Age         *int               `json:"age"`
	Licenses    map[string]License `json:"licenses"`
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})))
	dec.DisallowUnknownFields() // catches typos in field names
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	return nil
}

func (m Meta) license(role string) (*License, string) {
	l, ok := m.Licenses[role]
	if !ok {
		l, ok = m.Licenses["*"]
	}
	if !ok || l.SPDX == "NOLICENSE" {
		return &License{SPDX: "NOLICENSE"}, ""
	}
	if l.SPDX == "" {
		return nil, fmt.Sprintf(`%s: the license for %q has no "spdx"`, metaName, role)
	}
	if l.URL == "" {
		l.URL = ccURL(l.SPDX)
	}
	if l.URL == "" {
		return nil, fmt.Sprintf(`%s: license %s needs a "url" (only Creative Commons ids get one automatically)`, metaName, l.SPDX)
	}
	return &l, ""
}

type builder struct {
	songsDir string
	blobs    map[string]string // sha256 hex -> source file
	sizes    map[string]int64
	skipped  []string
	warnings []string
}

func (b *builder) warn(format string, a ...any) {
	b.warnings = append(b.warnings, fmt.Sprintf(format, a...))
}
func (b *builder) skip(format string, a ...any) {
	b.skipped = append(b.skipped, fmt.Sprintf(format, a...))
}

// catalog collects all charts below one catalog folder.
func (b *builder) catalog(dir string) ([]Song, map[string]string, error) {
	var songs []Song
	moved := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		if p != dir && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		return b.folder(p, &songs, moved)
	})
	return songs, moved, err
}

func (b *builder) folder(dir string, songs *[]Song, moved map[string]string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	rel, _ := filepath.Rel(b.songsDir, dir)
	files := map[string]bool{}
	var txts []string
	charts := map[string]*chart{}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		files[e.Name()] = true
		if strings.EqualFold(filepath.Ext(e.Name()), ".txt") {
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				return err
			}
			if c := parseChart(data); c != nil {
				txts = append(txts, e.Name())
				charts[e.Name()] = c
			}
		}
	}
	if len(txts) == 0 {
		return nil
	}
	var meta Meta
	if files[metaName] {
		if err := readJSON(filepath.Join(dir, metaName), &meta); err != nil {
			b.skip("%s: %v", rel, err)
			return nil
		}
	}
	for role := range meta.Licenses {
		if _, ok := roleExt[role]; (!ok && role != "*") || role == "license" {
			b.skip(`%s: %s has a license for the unknown role %q`, rel, metaName, role)
			return nil
		}
	}
	if len(txts) > 1 && (meta.ID != "" || len(meta.PreviousIDs) > 0) {
		b.skip(`%s: %s uses "id"/"previous_ids" but the folder has %d charts; use "ids": {"<file>.txt": "<id>"} instead`, rel, metaName, len(txts))
		return nil
	}
	for _, txt := range txts {
		id := meta.IDs[txt]
		if id == "" {
			id = meta.ID
		}
		song, problem := b.entry(dir, rel, files, txt, charts[txt], meta, id)
		if problem != "" {
			b.skip("%s: %s", filepath.Join(rel, txt), problem)
			continue
		}
		*songs = append(*songs, *song)
		for _, old := range meta.PreviousIDs {
			moved[old] = song.ID
		}
	}
	return nil
}

func (b *builder) entry(dir, rel string, files map[string]bool, txt string, c *chart, meta Meta, id string) (*Song, string) {
	h := c.headers
	if h["ARTIST"] == "" {
		return nil, "the txt has no #ARTIST"
	}
	if id == "" {
		id = slug(h["ARTIST"] + "-" + h["TITLE"])
	}
	if !idRe.MatchString(id) {
		return nil, fmt.Sprintf(`no valid id (got %q); set "id" in %s (a-z, 0-9, '.', '_', '-', at most 64 characters)`, id, metaName)
	}
	where := filepath.Join(rel, txt)
	if c.note != "" {
		b.warn("%s: %s; published unchanged", where, c.note)
	}
	onDisk := map[string]string{}
	for f := range files {
		onDisk[strings.ToLower(f)] = f
	}
	song := &Song{ID: id, Artist: h["ARTIST"], Title: h["TITLE"], Rendition: h["RENDITION"], Language: splitList(h["LANGUAGE"]),
		Genre: splitList(h["GENRE"]), Edition: h["EDITION"], Tags: splitList(h["TAGS"]), Creator: splitList(h["CREATOR"]),
		Age: meta.Age, Voices: c.voices, Files: []File{}}
	if y, ok := parseNum(h["YEAR"]); ok {
		song.Year = int(y)
	}
	if bpm, ok := parseNum(h["BPM"]); ok && bpm > 0 {
		gap, _ := parseNum(h["GAP"])
		song.Duration = math.Round((gap/1000+float64(c.lastBeat)*60/(bpm*4))*10) / 10
	}
	if p, ok := parseNum(h["PREVIEWSTART"]); ok {
		song.PreviewStart = &p
	}

	add := func(role, name string, required bool) string {
		if strings.Contains(name, "://") {
			b.warn("%s: #%s is a URL, not a file; not published", where, strings.ToUpper(role))
			return ""
		}
		if p := nameProblem(name, role); p != "" {
			return fmt.Sprintf("the %s file name %q %s", role, name, p)
		}
		real := name
		if !files[name] {
			var ok bool
			if real, ok = onDisk[strings.ToLower(name)]; !ok {
				if required {
					return fmt.Sprintf("the %s file %q is missing", role, name)
				}
				b.warn("%s: the %s file %q is missing; published without it", where, role, name)
				return ""
			}
			b.warn("%s: the %s reference %q differs from the file %q only by upper/lower case; published under the txt's spelling", where, role, name, real)
		}
		if hasCombiningMark(name) {
			b.warn("%s: the file name %q contains combining accents (probably not Unicode NFC); rename the file and its reference", where, name)
		}
		for _, f := range song.Files {
			if strings.EqualFold(f.Name, name) {
				return "" // the same file referenced twice (e.g. cover = background)
			}
		}
		src := filepath.Join(dir, real)
		sum, size, err := hashFile(src)
		if err != nil {
			return err.Error()
		}
		b.blobs[sum], b.sizes[sum] = src, size
		if size > 512<<20 {
			b.warn("%s: %q is %d MB; Cloudflare (Free, Pro, Business) does not cache files over 512 MB", where, name, size>>20)
		}
		f := File{Role: role, Name: name, SHA256: "sha256:" + sum, Size: size, Type: mimeTypes[strings.ToLower(filepath.Ext(name))]}
		if role != "license" {
			lic, problem := meta.license(role)
			if problem != "" {
				return problem
			}
			f.License = lic
		}
		song.Files = append(song.Files, f)
		return ""
	}

	if p := add("txt", txt, true); p != "" {
		return nil, p
	}
	audio := h["AUDIO"]
	if audio == "" {
		audio = h["MP3"]
	} else if h["MP3"] != "" && h["MP3"] != audio {
		b.warn("%s: #AUDIO and #MP3 name different files; only the #AUDIO file is published", where)
	}
	if audio == "" {
		return nil, "the txt has no #AUDIO or #MP3"
	}
	if p := add("audio", audio, true); p != "" {
		return nil, p
	}
	if !slices.ContainsFunc(song.Files, func(f File) bool { return f.Role == "audio" }) {
		return nil, "the audio is not a local file"
	}
	for _, r := range [][2]string{{"VOCALS", "vocals"}, {"INSTRUMENTAL", "instrumental"}, {"VIDEO", "video"}, {"COVER", "cover"}, {"BACKGROUND", "background"}} {
		if v := h[r[0]]; v != "" {
			if p := add(r[1], v, false); p != "" {
				return nil, p
			}
		}
	}
	for _, n := range []string{"license.txt", "license.md"} {
		if real, ok := onDisk[n]; ok {
			if p := add("license", real, false); p != "" {
				return nil, p
			}
		}
	}
	return song, ""
}

// previous is what the last build left in the output folder.
type previous struct {
	feed     *Feed
	raw      []byte
	songs    map[string]string   // id -> JSON of the entry
	audio    map[string]string   // id -> audio hash
	files    map[string][]string // id -> blob hashes (hex)
	catalogs []string
}

func loadPrevious(out string) (*previous, error) {
	raw, err := os.ReadFile(filepath.Join(out, "feed.json"))
	if errors.Is(err, fs.ErrNotExist) {
		if entries, _ := os.ReadDir(out); len(entries) > 0 {
			return nil, fmt.Errorf("%s is not empty and has no feed.json; refusing to write a feed into it", out)
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p := &previous{feed: &Feed{}, raw: raw, songs: map[string]string{}, audio: map[string]string{}, files: map[string][]string{}}
	if err := json.Unmarshal(raw, p.feed); err != nil {
		return nil, fmt.Errorf("%s/feed.json: %v", out, err)
	}
	for _, ref := range p.feed.Catalogs {
		p.catalogs = append(p.catalogs, ref.Path)
		stored, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(ref.Path)))
		if err != nil {
			continue
		}
		cat, err := decodeCatalog(ref, stored)
		if err != nil {
			continue
		}
		for _, s := range cat.Songs {
			p.songs[s.ID] = string(marshal(s, false))
			for _, f := range s.Files {
				if f.Role == "audio" {
					p.audio[s.ID] = f.SHA256
				}
				p.files[s.ID] = append(p.files[s.ID], strings.TrimPrefix(f.SHA256, "sha256:"))
			}
		}
	}
	return p, nil
}

func readRemoved(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for n, line := range strings.Split(string(data), "\n") {
		line, _, _ = strings.Cut(line, "#")
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if !idRe.MatchString(line) {
			return nil, fmt.Errorf("%s line %d: %q is not a song id", path, n+1, line)
		}
		if !slices.Contains(ids, line) {
			ids = append(ids, line)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func linkOrCopy(src, dst string, copyOnly bool) error {
	if st, err := os.Stat(dst); err == nil && st.Mode().IsRegular() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if !copyOnly && os.Link(src, dst) == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst + ".tmp")
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(dst+".tmp", dst)
}

var (
	catalogFileRe = regexp.MustCompile(`^[0-9a-f]{64}\.json(\.gz)?$`)
	sigFileRe     = regexp.MustCompile(`^[0-9a-f]{64}\.sig$`)
)

// prune deletes generated files in dir that match pattern and are not in keep. It never touches anything else.
func prune(dir string, pattern *regexp.Regexp, keep map[string]bool) int {
	n := 0
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && pattern.MatchString(d.Name()) && !keep[d.Name()] && os.Remove(p) == nil {
			n++
		}
		return nil
	})
	return n
}

func list(ids []string) string {
	sort.Strings(ids)
	if len(ids) > 12 {
		return strings.Join(ids[:12], ", ") + fmt.Sprintf(" … and %d more", len(ids)-12)
	}
	return strings.Join(ids, ", ")
}

func totalBlobBytes(b *builder) int64 {
	var n int64
	for _, s := range b.sizes {
		n += s
	}
	return n
}

func cmdBuild(args []string) error {
	fl := flag.NewFlagSet("build", flag.ContinueOnError)
	out := fl.String("out", "", "published folder")
	keyFile := fl.String("key", os.Getenv("KFEED_KEY"), "signing key file, or - to read it from standard input")
	dry := fl.Bool("dry-run", false, "write nothing")
	mass := fl.Bool("allow-mass-removal", false, "continue although many songs disappeared")
	copyOnly := fl.Bool("copy", false, "copy instead of hard-linking")
	pos, err := parse(fl, args)
	if err != nil || len(pos) != 1 {
		return errors.New("usage: kfeed build <feed-folder> [--key file|-] [--out folder] [--dry-run] [--allow-mass-removal] [--copy]")
	}
	return build(pos[0], *out, *keyFile, *dry, *mass, *copyOnly, os.Stdout)
}

func build(dir, out, keyFile string, dry, mass, copyOnly bool, w io.Writer) error {
	var cfg Config
	if err := readJSON(filepath.Join(dir, configName), &cfg); err != nil {
		return err
	}
	if cfg.Name == "" || cfg.Operator.Name == "" {
		return fmt.Errorf(`%s: "name" and "operator.name" must be set`, configName)
	}
	if err := checkContact(cfg.Operator.Contact); err != nil || strings.HasSuffix(cfg.Operator.Contact, "@example.org") {
		return fmt.Errorf(`%s: set "operator.contact" to a real mailto: or https: address (it is where takedown requests go)`, configName)
	}
	for _, m := range cfg.Mirrors {
		if err := checkMirror(m); err != nil {
			return fmt.Errorf("%s: mirror %v", configName, err)
		}
	}
	if cfg.Homepage != "" && !strings.HasPrefix(cfg.Homepage, "https://") && !strings.HasPrefix(cfg.Homepage, "http://") {
		return fmt.Errorf(`%s: "homepage" must be an http(s) address`, configName)
	}
	if cfg.URL != "" {
		if err := checkMirror(cfg.URL); err != nil {
			return fmt.Errorf("%s: url %v", configName, err)
		}
		if !strings.HasSuffix(cfg.URL, "feed.json") {
			cfg.URL = strings.TrimSuffix(cfg.URL, "/") + "/feed.json"
		}
	}
	if len(cfg.Keys) > 8 {
		return fmt.Errorf("%s: at most 8 keys", configName)
	}
	for _, k := range cfg.Keys {
		if _, err := parsePub(k); err != nil {
			return fmt.Errorf("%s: %v", configName, err)
		}
	}
	if cfg.Private {
		// SPEC 5.5: feed.json is the same file for every subscriber, so it must not name addresses, and only a
		// signed feed lets a subscriber move to a new personal address.
		switch {
		case len(cfg.Keys) == 0:
			return fmt.Errorf(`%s: a private feed must be signed: add "keys" (see docs/signing.md)`, configName)
		case len(cfg.Mirrors) > 0 || cfg.URL != "" || cfg.MovedTo != "":
			return fmt.Errorf(`%s: a private feed can have no "mirrors", "url" or "moved_to": every subscriber uses a personal address`, configName)
		}
	}
	var priv ed25519.PrivateKey
	switch {
	case len(cfg.Keys) > 0 && keyFile == "":
		return fmt.Errorf(`this feed is signed ("keys" in %s): pass --key <key file>, or --key - to read the key from standard input. To publish unsigned, remove "keys" first`, configName)
	case keyFile != "":
		var err error
		if priv, err = loadKey(keyFile); err != nil {
			return err
		}
		if pub := pubString(priv.Public().(ed25519.PublicKey)); !slices.Contains(cfg.Keys, pub) {
			return fmt.Errorf(`the key %s is not listed in "keys" of %s; subscribers would reject the feed. Add:\n  %q`, keyFile, configName, pub)
		}
	}
	resolve := func(p, def string) string {
		if p == "" {
			p = def
		}
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}
	songsDir := resolve(cfg.Songs, "songs")
	if out == "" {
		out = resolve(cfg.Output, "public")
	}
	removed, err := readRemoved(filepath.Join(dir, removedName))
	if err != nil {
		return err
	}
	prev, err := loadPrevious(out)
	if err != nil {
		return err
	}

	// 1. read the songs folder: every top-level folder is one catalog
	b := &builder{songsDir: songsDir, blobs: map[string]string{}, sizes: map[string]int64{}}
	top, err := os.ReadDir(songsDir)
	if err != nil {
		return fmt.Errorf("songs folder: %v", err)
	}
	type cat struct {
		ref   CatalogRef
		songs []Song
		moved map[string]string
	}
	var cats []cat
	owner := map[string]string{} // id -> catalog id
	for _, e := range top {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			if strings.EqualFold(filepath.Ext(e.Name()), ".txt") {
				b.warn("%s: files directly in the songs folder are ignored; put songs into a catalog folder", e.Name())
			}
			continue
		}
		cc := cfg.Catalogs[e.Name()]
		if cc.ID == "" {
			cc.ID = slug(e.Name())
		}
		if !idRe.MatchString(cc.ID) {
			return fmt.Errorf(`catalog folder %q needs an id: add "catalogs": {%q: {"id": "..."}} to %s`, e.Name(), e.Name(), configName)
		}
		names := map[string]string{"en": e.Name()}
		for k, v := range cc.Names {
			names[k] = v
		}
		songs, moved, err := b.catalog(filepath.Join(songsDir, e.Name()))
		if err != nil {
			return err
		}
		kept := songs[:0]
		for _, s := range songs {
			if other, dup := owner[s.ID]; dup {
				b.skip(`%s / %s: the id %q is already used (catalog %s). Same artist and title? Set different "id"s in %s`, e.Name(), s.Title, s.ID, other, metaName)
				continue
			}
			owner[s.ID] = cc.ID
			kept = append(kept, s)
		}
		if len(kept) == 0 {
			b.warn("catalog folder %q has no publishable songs; left out", e.Name())
			continue
		}
		sort.Slice(kept, func(i, j int) bool { return kept[i].ID < kept[j].ID })
		for old, id := range moved {
			if _, live := owner[old]; live || owner[id] == "" {
				delete(moved, old)
			}
		}
		cats = append(cats, cat{ref: CatalogRef{ID: cc.ID, Names: names}, songs: kept, moved: moved})
	}
	if len(cats) == 0 {
		return fmt.Errorf("no songs found in %s (expected <songs>/<catalog folder>/<song folder>/<song>.txt)", songsDir)
	}
	for _, id := range removed {
		if _, live := owner[id]; live {
			return fmt.Errorf("%q is listed in %s but its song is still in the songs folder. Delete the song folder, or remove the line", id, removedName)
		}
	}

	// 2. compare with the last build
	var added, changed, delisted, withdrawn, undeclared, hints []string
	current := map[string]string{}
	audioNow := map[string]string{}
	for _, c := range cats {
		for _, s := range c.songs {
			current[s.ID] = string(marshal(s, false))
			for _, f := range s.Files {
				if f.Role == "audio" {
					audioNow[f.SHA256] = s.ID
				}
				if f.License != nil && f.License.SPDX == "NOLICENSE" && !slices.Contains(undeclared, s.ID) {
					undeclared = append(undeclared, s.ID)
				}
			}
		}
	}
	movedAll := map[string]string{}
	for _, c := range cats {
		for old, id := range c.moved {
			movedAll[old] = id
		}
	}
	if prev != nil {
		for id, js := range current {
			if old, ok := prev.songs[id]; !ok {
				added = append(added, id)
			} else if old != js {
				changed = append(changed, id)
			}
		}
		for id := range prev.songs {
			if _, ok := current[id]; ok {
				continue
			}
			switch {
			case slices.Contains(removed, id):
				withdrawn = append(withdrawn, id)
			case movedAll[id] != "":
			default:
				delisted = append(delisted, id)
				if now := audioNow[prev.audio[id]]; now != "" {
					hints = append(hints, fmt.Sprintf(`%q is gone and %q has the same audio. If it is the same song, add "previous_ids": [%q] to its %s so playlists follow`, id, now, id, metaName))
				}
			}
		}
		if n := len(delisted); n > 5 && n*5 > len(prev.songs) && !mass {
			return fmt.Errorf("%d of %d published songs disappeared (%s). If the songs folder is complete and this is intended, run again with --allow-mass-removal", n, len(prev.songs), list(delisted))
		}
	}

	catSongs := func() [][]Song {
		all := [][]Song{}
		for _, c := range cats {
			all = append(all, c.songs)
		}
		return all
	}

	// 3. catalogs and feed.json
	now := time.Now().UTC()
	feed := Feed{Spec: Spec, Name: cfg.Name, Description: cfg.Description, Homepage: cfg.Homepage, Operator: cfg.Operator,
		Keys: cfg.Keys, Mirrors: cfg.Mirrors, MovedTo: cfg.MovedTo, Removed: removed}
	stored := map[string][]byte{}
	for _, c := range cats {
		data := gzipBytes(marshal(Catalog{Spec: Spec, ID: c.ref.ID, Songs: c.songs, Moved: c.moved}, false))
		sum := hashBytes(data)
		seen := map[string]bool{}
		for _, s := range c.songs {
			for _, f := range s.Files {
				if !seen[f.SHA256] {
					seen[f.SHA256] = true
					c.ref.Bytes += f.Size
				}
			}
		}
		c.ref.Path, c.ref.SHA256, c.ref.Size, c.ref.Songs = "catalog/"+sum+".json.gz", "sha256:"+sum, int64(len(data)), len(c.songs)
		stored[c.ref.Path] = data
		feed.Catalogs = append(feed.Catalogs, c.ref)
	}
	if !dry { // blobs first; also repairs a published folder that lost files
		for sum, src := range b.blobs {
			if err := linkOrCopy(src, filepath.Join(out, "blobs", sum[:2], sum), copyOnly); err != nil {
				return err
			}
		}
	}
	if prev != nil { // unchanged content keeps the old sequence, so a rebuild without changes writes no new feed.json
		feed.Sequence, feed.Generated = prev.feed.Sequence, prev.feed.Generated
		_, sigErr := os.Stat(filepath.Join(out, "sig", hashBytes(prev.raw)+".sig"))
		if bytes.Equal(marshal(feed, true), prev.raw) && (priv == nil || sigErr == nil) {
			if !dry {
				if err := writeSite(out, cfg.URL, cfg.Private, feed, catSongs(), totalBlobBytes(b)); err != nil {
					return err
				}
			}
			fmt.Fprintf(w, "No changes since the last build (%d songs, sequence %d). No new feed.json written.\n", len(current), feed.Sequence)
			if !dry {
				// The catalog and signature of the build before the last one were kept for one more build. That
				// build is this one, so they go now: a withdrawn song must not stay listed in an old catalog.
				keepCat, keepSig := map[string]bool{}, map[string]bool{hashBytes(prev.raw) + ".sig": true}
				for p := range stored {
					keepCat[filepath.Base(p)] = true
				}
				if n := prune(filepath.Join(out, "catalog"), catalogFileRe, keepCat) + prune(filepath.Join(out, "sig"), sigFileRe, keepSig); n > 0 {
					fmt.Fprintf(w, "Removed %d outdated file(s) of the build before (song lists, signatures). Upload again so they disappear from the server too.\n", n)
				}
			}
			report(w, b, nil, nil)
			return nil
		}
	}
	feed.Sequence, feed.Generated = now.Unix(), now.Format(time.RFC3339)
	if prev != nil && feed.Sequence <= prev.feed.Sequence {
		feed.Sequence = prev.feed.Sequence + 1
	}
	feedBytes := marshal(feed, true)

	// 4. write: catalogs, signature, feed.json last (blobs are already there)
	pruned := 0
	if !dry {
		for p, data := range stored {
			if err := os.MkdirAll(filepath.Join(out, "catalog"), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(out, filepath.FromSlash(p)), data, 0o644); err != nil {
				return err
			}
		}
		keepSig := map[string]bool{}
		if priv != nil {
			name := hashBytes(feedBytes) + ".sig"
			keepSig[name] = true
			if err := os.MkdirAll(filepath.Join(out, "sig"), 0o755); err != nil {
				return err
			}
			sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, feedBytes)) + "\n"
			if err := os.WriteFile(filepath.Join(out, "sig", name), []byte(sig), 0o644); err != nil {
				return err
			}
		}
		tmp := filepath.Join(out, "feed.json.tmp")
		if err := os.WriteFile(tmp, feedBytes, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(out, "feed.json")); err != nil {
			return err
		}
		if err := writeSite(out, cfg.URL, cfg.Private, feed, catSongs(), totalBlobBytes(b)); err != nil {
			return err
		}
		// 5. clean up generated files nobody references any more (the previous catalog and signature stay one more build)
		keepCat, keepBlob := map[string]bool{}, map[string]bool{}
		for p := range stored {
			keepCat[filepath.Base(p)] = true
		}
		if prev != nil {
			keepSig[hashBytes(prev.raw)+".sig"] = true
			for _, p := range prev.catalogs {
				keepCat[filepath.Base(p)] = true
			}
		}
		for sum := range b.blobs {
			keepBlob[sum] = true
		}
		pruned = prune(filepath.Join(out, "blobs"), hexRe, keepBlob) +
			prune(filepath.Join(out, "catalog"), catalogFileRe, keepCat) +
			prune(filepath.Join(out, "sig"), sigFileRe, keepSig)
	}

	// 6. report
	verb := "Built"
	if dry {
		verb = "DRY RUN, nothing written. Would build"
	}
	signed := "UNSIGNED"
	if priv != nil {
		signed = "signed by " + pubString(priv.Public().(ed25519.PublicKey))
	}
	fmt.Fprintf(w, "%s feed %q → %s\n  sequence %d, %s\n", verb, cfg.Name, out, feed.Sequence, signed)
	for _, c := range feed.Catalogs {
		fmt.Fprintf(w, "  catalog %-24s %5d songs %8.1f MB\n", c.ID, c.Songs, float64(c.Bytes)/1e6)
	}
	if prev == nil {
		fmt.Fprintf(w, "First build: %d songs.\n", len(current))
	} else {
		fmt.Fprintln(w, "Changes since the last build:")
		for _, l := range []struct {
			mark, what, note string
			ids              []string
		}{{"+", "added", "", added}, {"~", "changed", "", changed},
			{"-", "delisted", " (clients hide them, cached files stay)", delisted},
			{"x", "withdrawn", " (clients delete them)", withdrawn}} {
			if len(l.ids) > 0 {
				fmt.Fprintf(w, "  %s %s%s (%d): %s\n", l.mark, l.what, l.note, len(l.ids), list(l.ids))
			}
		}
		if len(added)+len(changed)+len(delisted)+len(withdrawn) == 0 {
			fmt.Fprintln(w, "  no song changed (feed settings did)")
		}
		if pruned > 0 {
			fmt.Fprintf(w, "  %d unused files deleted from the published folder\n", pruned)
		}
		var purge []string
		for _, id := range withdrawn {
			for _, sum := range prev.files[id] {
				if _, used := b.blobs[sum]; !used && len(sum) == 64 {
					purge = append(purge, "blobs/"+sum[:2]+"/"+sum)
				}
			}
		}
		if len(purge) > 0 {
			sort.Strings(purge)
			fmt.Fprintf(w, "Withdrawn files. Delete them on every server and mirror, and purge them from CDN caches:\n  %s\n", strings.Join(purge, "\n  "))
		}
	}
	report(w, b, undeclared, hints)
	return nil
}

func report(w io.Writer, b *builder, undeclared, hints []string) {
	if len(undeclared) > 0 {
		fmt.Fprintf(w, "Hidden by default in clients, no license declared (%d): %s\n  → add a %s with \"licenses\" to these song folders\n", len(undeclared), list(undeclared), metaName)
	}
	for _, h := range hints {
		fmt.Fprintln(w, "Renamed?", h)
	}
	if len(b.skipped) > 0 {
		fmt.Fprintf(w, "NOT PUBLISHED, fix these (%d):\n", len(b.skipped))
		for _, s := range b.skipped {
			fmt.Fprintln(w, "  !", s)
		}
	}
	if len(b.warnings) > 0 {
		fmt.Fprintf(w, "Warnings (%d):\n", len(b.warnings))
		for _, s := range b.warnings {
			fmt.Fprintln(w, "  -", s)
		}
	}
}

const configTemplate = `{
  "name": "My Karaoke Feed",
  "description": "",
  "homepage": "",
  "operator": { "name": "", "contact": "mailto:you@example.org" },
  "songs": "songs",
  "output": "public",
  "keys": [],
  "mirrors": [],
  "url": ""
}
`

const metaTemplate = `{
  "licenses": {
    "*":     { "spdx": "CC-BY-4.0", "attribution": "'Song Title' by Artist Name", "source": "https://where-the-song-was-published.example/" },
    "txt":   { "spdx": "CC-BY-4.0", "attribution": "UltraStar notes by Creator Name", "source": "https://..." }
  },
  "age": 0
}
`

const removedTemplate = `# Song ids that were withdrawn (takedown). One id per line, text after # is a comment.
# Clients of a signed feed delete their cached copies of these songs.
# To merely stop offering a song, just delete its folder and do not list it here.
`

func cmdInit(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: kfeed init <feed-folder>")
	}
	dir := args[0]
	if _, err := os.Stat(filepath.Join(dir, configName)); err == nil {
		return fmt.Errorf("%s already has a %s", dir, configName)
	}
	if err := os.MkdirAll(filepath.Join(dir, "songs"), 0o755); err != nil {
		return err
	}
	for name, content := range map[string]string{configName: configTemplate, "feed-meta.example.json": metaTemplate, removedName: removedTemplate} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("Created %s. Next:\n  1. edit %s (name, operator, contact)\n  2. put songs into %s/<Category Name>/<song folder>/\n  3. kfeed build %s\n", dir, configName, filepath.Join(dir, "songs"), dir)
	return nil
}
