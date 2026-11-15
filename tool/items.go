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

// planItem decides what to download for one wanted item. claimed holds the target paths (lower case) that earlier
// items of this sync use: the first item wins (SPEC 5.6 rule 5).
func planItem(o syncOpts, st *syncState, id string, it Item, catalog string, dir string, claimed map[string]string, rep *syncReport) *songPlan {
	o = itemOpts(o)
	dir = filepath.Clean(dir)
	for _, f := range it.Files {
		key := strings.ToLower(filepath.Join(dir, filepath.FromSlash(f.Path)))
		if other, used := claimed[key]; used && other != id {
			rep.add(&rep.unsafe, fmt.Sprintf("%s: %s is also a file of %s", id, f.Path, other))
			return nil
		}
	}
	rec := st.Items[id]
	if rec != nil && filepath.Clean(rec.Dir) != dir { // the folder for this kind changed: start again there
		removeItem(id, rec, rep)
		delete(st.Items, id)
		rec = nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		rep.add(&rep.failed, fmt.Sprintf("%s: %v", id, err))
		return nil
	}
	old := &songRec{Files: map[string]fileRec{}}
	if rec != nil {
		old.Files = rec.Files
	}
	p := &songPlan{id: id, folder: dir, catalog: catalog, song: Song{Title: itemName(it)}, isNew: rec == nil,
		keep: map[string]fileRec{}, item: &it}
	for _, f := range it.Files {
		claimed[strings.ToLower(filepath.Join(dir, filepath.FromSlash(f.Path)))] = id
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

// finishItem records an item after the downloads, like finishSong for songs (without hiding anything).
func finishItem(st *syncState, p *songPlan, rep *syncReport) {
	rec := st.Items[p.id]
	files := map[string]fileRec{}
	for name, f := range p.keep {
		files[name] = f
	}
	folders := map[string]bool{}
	for _, j := range p.jobs {
		folders[filepath.Dir(j.path)] = true
		if j.done {
			files[j.name] = record(j.path, j.hex)
		} else if rec != nil {
			if old, ok := rec.Files[j.name]; ok {
				files[j.name] = old // update failed: the old file stays
			}
		}
	}
	done := !p.failed && !slices.ContainsFunc(p.jobs, func(j *fileJob) bool { return !j.done })
	if done && rec != nil {
		for name, old := range rec.Files { // files the item no longer has: deleted unless changed here
			if _, still := files[name]; !still && safeItemPath(name) {
				removeItemFile(p.folder, name, old, rep, p.id)
			}
		}
	}
	st.Items[p.id] = &itemRec{Catalog: p.catalog, Dir: p.folder, Files: files}
	if !done {
		return
	}
	for f := range folders {
		removeParts(f)
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

// removeItem deletes the files the client wrote for an item, except files changed here.
func removeItem(id string, rec *itemRec, rep *syncReport) {
	for name, old := range rec.Files {
		if safeItemPath(name) {
			removeItemFile(rec.Dir, name, old, rep, id)
		}
	}
}

// removeItemFile deletes one unchanged file of an item and the folders that became empty below dir (never dir).
func removeItemFile(dir, name string, old fileRec, rep *syncReport, id string) {
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
	for p := path.Dir(name); p != "."; p = path.Dir(p) {
		folder := filepath.Join(dir, filepath.FromSlash(p))
		removeParts(folder)
		if os.Remove(folder) != nil {
			return
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
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
	return targets, nil
}
