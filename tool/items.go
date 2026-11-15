package kfeed

// Packs of other kinds than songs (SPEC 4.4, 5.6), for example themes. Their files go into a folder the client
// chooses per kind (a Target), and only from a signed feed whose key the client passed itself.

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Target says where the files of one kind go and which file extensions may be stored there.
type Target struct {
	Dir        string
	Extensions []string // lower case with the dot, for example ".ini"; the block list of SPEC 5.6 always applies
}

// itemRec is what the client stored of an installed item. Dir is the target folder at install time, so that the
// files can be removed later even when no target is given any more.
type itemRec struct {
	Catalog string             `json:"catalog"`
	Dir     string             `json:"dir"`
	Files   map[string]fileRec `json:"files"` // key: path with "/"
}

// itemName is what progress reports show for an item.
func itemName(it Item) string {
	if n := it.Names["en"]; n != "" {
		return n
	}
	for _, n := range it.Names {
		return n
	}
	return it.ID
}

// safeItemPath reports whether a stored path is still one the item rules allow (the state file is not trusted to
// point outside the target folder).
func safeItemPath(p string) bool {
	parts := strings.Split(p, "/")
	return len(parts) <= 8 && !slices.ContainsFunc(parts, func(s string) bool { return partProblem(s) != "" })
}

// itemOpts are the sync options for the files of items: a file at an item's path is always replaced with the feed's
// version, also one the client did not write (an older copy of the same theme installed by hand) or one changed
// here. Files at other paths are never touched (SPEC 5.6 rule 5).
func itemOpts(o syncOpts) syncOpts {
	o.replaceLocal = true
	return o
}

// claims are the target paths that items of this sync use (SPEC 5.6 rule 5: the first item wins). Keys are lower
// case full paths; files and the folders above them are kept apart, so that a file "a" and a file "a/b" of two items
// also count as a conflict.
type claims struct{ files, dirs map[string]string }

func newClaims() claims { return claims{files: map[string]string{}, dirs: map[string]string{}} }

func claimKey(dir, rel string) string {
	return strings.ToLower(filepath.Join(dir, filepath.FromSlash(rel)))
}

// conflict returns the other item that uses a path of it, or "".
func (c claims) conflict(id, dir string, it Item) (other, file string) {
	for _, f := range it.Files {
		key := claimKey(dir, f.Path)
		for _, o := range []string{c.files[key], c.dirs[key]} {
			if o != "" && o != id {
				return o, f.Path
			}
		}
		for d := path.Dir(f.Path); d != "."; d = path.Dir(d) {
			if o := c.files[claimKey(dir, d)]; o != "" && o != id {
				return o, f.Path
			}
		}
	}
	return "", ""
}

func (c claims) add(id, dir string, it Item) {
	for _, f := range it.Files {
		c.files[claimKey(dir, f.Path)] = id
		for d := path.Dir(f.Path); d != "."; d = path.Dir(d) {
			c.dirs[claimKey(dir, d)] = id
		}
	}
}

// ownedByOther reports whether another item uses the file name in dir: one planned in this sync (c), or one the state
// still records. Such a file is never deleted for this item.
func ownedByOther(st *syncState, c claims, id, dir, name string) bool {
	key := claimKey(dir, name)
	if o := c.files[key]; o != "" && o != id {
		return true
	}
	for oid, r := range st.Items {
		if oid == id || !strings.EqualFold(filepath.Clean(r.Dir), filepath.Clean(dir)) {
			continue
		}
		for n := range r.Files {
			if strings.EqualFold(n, name) {
				return true
			}
		}
	}
	return false
}

// linkFree reports whether no part of rel below dir (folders and the file itself) is a symbolic link, so that writing
// or deleting <dir>/<rel> stays inside dir. Parts that don't exist yet are fine.
func linkFree(dir, rel string) bool {
	cur := dir
	for _, part := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			return true
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

// planItem decides what to download for one wanted item. c holds the paths of the items planned before it: the
// first item wins (SPEC 5.6 rule 5).
func planItem(o syncOpts, st *syncState, id string, it Item, catalog string, dir string, c claims, rep *syncReport) *songPlan {
	o = itemOpts(o)
	dir = filepath.Clean(dir)
	if other, file := c.conflict(id, dir, it); other != "" {
		rep.add(&rep.unsafe, fmt.Sprintf("%s: %s is also a file or folder of %s", id, file, other))
		return nil
	}
	for _, f := range it.Files {
		if !linkFree(dir, f.Path) {
			rep.add(&rep.unsafe, fmt.Sprintf("%s: %s goes through a link in the target folder", id, f.Path))
			return nil
		}
	}
	rec := st.Items[id]
	if rec != nil && filepath.Clean(rec.Dir) != dir { // the folder for this kind changed: start again there
		removeItem(st, id, rec, rep)
		delete(st.Items, id)
		rec = nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		rep.add(&rep.failed, fmt.Sprintf("%s: %v", id, err))
		return nil
	}
	c.add(id, dir, it)
	old := &songRec{Files: map[string]fileRec{}}
	if rec != nil {
		old.Files = rec.Files
	}
	p := &songPlan{id: id, folder: dir, catalog: catalog, song: Song{Title: itemName(it)}, isNew: rec == nil,
		keep: map[string]fileRec{}, item: &it}
	for _, f := range it.Files {
		target := filepath.Join(dir, filepath.FromSlash(f.Path))
		keep, _, download := fileAction(o, target, File{Name: f.Path, SHA256: f.SHA256, Size: f.Size}, old)
		if !download {
			p.keep[f.Path] = keep
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			rep.add(&rep.failed, fmt.Sprintf("%s: %v", id, err))
			return nil
		}
		// Files in sub-folders first, top-level files last (runDownloads' second pass): a theme shows up in the game
		// when its top file exists, and that file only comes when everything else is there.
		p.jobs = append(p.jobs, &fileJob{name: f.Path, hex: strings.TrimPrefix(f.SHA256, "sha256:"), path: target,
			size: f.Size, txt: !strings.Contains(f.Path, "/"), plan: p})
	}
	return p
}

// finishItem records an item after the downloads, like finishSong for songs (without hiding anything). c holds the
// paths of all items planned in this sync.
func finishItem(st *syncState, c claims, p *songPlan, rep *syncReport) {
	rec := st.Items[p.id]
	files := map[string]fileRec{}
	for name, f := range p.keep {
		files[name] = f
	}
	for _, j := range p.jobs {
		if j.done {
			files[j.name] = record(j.path, j.hex)
		} else if rec != nil {
			if old, ok := rec.Files[j.name]; ok {
				files[j.name] = old // update failed: the old file stays
			}
		}
	}
	done := !p.failed && !slices.ContainsFunc(p.jobs, func(j *fileJob) bool { return !j.done })
	if rec != nil {
		for name, old := range rec.Files { // files the item no longer has
			if _, still := files[name]; still || !safeItemPath(name) {
				continue
			}
			switch {
			case !done:
				files[name] = old // deleted when the update is complete, so it stays recorded until then
			case ownedByOther(st, c, p.id, p.folder, name):
				// another item has the same path now: the file is its file
			default:
				removeItemFile(p.folder, name, old, rep, p.id)
			}
		}
	}
	st.Items[p.id] = &itemRec{Catalog: p.catalog, Dir: p.folder, Files: files}
	if !done {
		return
	}
	if rec != nil { // interrupted downloads of older versions of its files (current ones are complete)
		for name, old := range rec.Files {
			if cur, ok := files[name]; safeItemPath(name) && (!ok || cur.SHA256 != old.SHA256) {
				os.Remove(partPath(filepath.Join(p.folder, filepath.FromSlash(name)), old.SHA256))
			}
		}
	}
	switch {
	case p.isNew:
		rep.add(&rep.added, p.id)
	case len(p.jobs) > 0:
		rep.add(&rep.updated, p.id)
	default:
		rep.add(&rep.unchanged, p.id)
	}
}

// removeItem deletes the files the client wrote for an item, except files changed here and files another item uses.
func removeItem(st *syncState, id string, rec *itemRec, rep *syncReport) {
	for name, old := range rec.Files {
		if safeItemPath(name) && !ownedByOther(st, newClaims(), id, rec.Dir, name) {
			removeItemFile(rec.Dir, name, old, rep, id)
		}
	}
}

// removeItemFile deletes one unchanged file of an item and the folders that became empty below dir (never dir). A
// path through a symbolic link is left alone.
func removeItemFile(dir, name string, old fileRec, rep *syncReport, id string) {
	if !linkFree(dir, name) {
		return
	}
	full := filepath.Join(dir, filepath.FromSlash(name))
	have, ok := localHash(full, old, true)
	switch {
	case !ok:
	case have == old.SHA256:
		os.Remove(full)
	default:
		rep.add(&rep.keptLocal, id+"/"+name)
		return
	}
	os.Remove(partPath(full, old.SHA256)) // only this file's interrupted download; others in the folder stay
	for p := path.Dir(name); p != "."; p = path.Dir(p) {
		if os.Remove(filepath.Join(dir, filepath.FromSlash(p))) != nil {
			return
		}
	}
}

// targetsFlag collects "--target kind=folder" and "--extensions kind=.a,.b" of kfeed sync.
type targetsFlag map[string]string

func (t targetsFlag) String() string { return "" }
func (t targetsFlag) Set(v string) error {
	kind, value, ok := strings.Cut(v, "=")
	if !ok || !kindRe.MatchString(kind) || kind == KindSongs || value == "" {
		return fmt.Errorf("%q must look like <kind>=<value>, with a kind other than songs", v)
	}
	t[kind] = value
	return nil
}

// checkTargets checks the targets a caller passes: every kind needs a folder and at least one allowed extension
// (lower case, with the dot). The folders are made absolute, because the state remembers them for later removal.
func checkTargets(in map[string]Target) (map[string]Target, error) {
	if in == nil {
		return nil, nil
	}
	out := map[string]Target{}
	for kind, t := range in {
		if !kindRe.MatchString(kind) || kind == KindSongs {
			return nil, fmt.Errorf("target %q: not a pack kind other than songs", kind)
		}
		if t.Dir == "" || len(t.Extensions) == 0 {
			return nil, fmt.Errorf("target %s needs a folder and at least one file extension", kind)
		}
		for _, e := range t.Extensions {
			if len(e) < 2 || e[0] != '.' || e != strings.ToLower(e) {
				return nil, fmt.Errorf("target %s: extension %q must be lower case and start with a dot, like .png", kind, e)
			}
		}
		dir, err := filepath.Abs(t.Dir)
		if err != nil {
			return nil, fmt.Errorf("target %s: %v", kind, err)
		}
		out[kind] = Target{Dir: dir, Extensions: slices.Clone(t.Extensions)}
	}
	return out, nil
}

// cliTargets combines the two flags; every kind needs both.
func cliTargets(dirs, exts targetsFlag) (map[string]Target, error) {
	targets := map[string]Target{}
	for kind, dir := range dirs {
		list, ok := exts[kind]
		if !ok {
			return nil, fmt.Errorf("--target %s needs --extensions %s=.ext,... too", kind, kind)
		}
		t := Target{Dir: dir}
		for _, e := range strings.Split(strings.ToLower(list), ",") {
			if e = strings.TrimSpace(e); !strings.HasPrefix(e, ".") || len(e) < 2 {
				return nil, fmt.Errorf("--extensions %s: %q must start with a dot, like .png", kind, e)
			}
			t.Extensions = append(t.Extensions, e)
		}
		targets[kind] = t
	}
	for kind := range exts {
		if _, ok := dirs[kind]; !ok {
			return nil, fmt.Errorf("--extensions %s needs --target %s=<folder> too", kind, kind)
		}
	}
	return checkTargets(targets)
}
