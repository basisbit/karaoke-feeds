package kfeed

// kfeed sync: the player side. Keeps a library folder in step with a feed (SPEC section 5).

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	stateDirName = ".kfeed"
	hiddenSuffix = ".kfeed-hidden" // a txt renamed like this is ignored by the games (they look for *.txt)
	partPrefix   = ".kfeed-part-"
)

var errChooseCatalogs = errors.New("choose what to sync")

type fileRec struct {
	SHA256 string `json:"sha256"` // hex
	Size   int64  `json:"size"`
	MTime  int64  `json:"mtime"` // of the file when it was last hashed; a cheap "unchanged" check
}

type songRec struct {
	Catalog string             `json:"catalog"`
	Txt     string             `json:"txt"`
	Hidden  bool               `json:"hidden,omitempty"`
	Files   map[string]fileRec `json:"files"`
}

type syncState struct {
	Locations         []string            `json:"locations"`
	Trust             []string            `json:"trust"`
	Sequence          int64               `json:"sequence"`
	FeedSHA           string              `json:"feed_sha256"`
	Catalogs          []string            `json:"catalogs"` // nil: not chosen yet; ["*"]: all, also future ones
	IncludeUndeclared bool                `json:"include_undeclared"`
	Exclude           []string            `json:"exclude,omitempty"` // license properties the player doesn't want: nc, nd, sa
	Songs             map[string]*songRec `json:"songs"`
	Items             map[string]*itemRec `json:"items,omitempty"` // items of other kinds (items.go)
}

type syncOpts struct {
	feed              string   // address given on the command line ("" = use the stored one)
	lib               string   // library folder
	catalogs          []string // nil = keep the stored choice
	all               bool
	includeUndeclared *bool
	exclude           *[]string
	key               string
	replaceLocal      bool
	jobs              int
	ctx               context.Context   // nil: not cancellable (command line)
	progress          *tracker          // nil: no progress reports (command line)
	seeds             []string          // folders with songs the player has already (seed.go)
	targets           map[string]Target // kind -> where its items go; kinds without one are left alone
}

func (o syncOpts) context() context.Context {
	if o.ctx == nil {
		return context.Background()
	}
	return o.ctx
}

type syncReport struct {
	mu                                                   sync.Mutex
	feedNote                                             string
	added, updated, unchanged, hidden, unhidden, removed []string
	withdrawn, moved, keptLocal, unsafe, undeclared      []string
	excluded                                             []string
	skippedPacks                                         []string // packs of another kind that were left alone
	failed                                               []string
	downloaded                                           int64
	imported                                             int64 // bytes taken from seed folders
	retried                                              int
}

func (r *syncReport) add(list *[]string, s string) {
	r.mu.Lock()
	*list = append(*list, s)
	r.mu.Unlock()
}

type catalogsFlag []string

func (c *catalogsFlag) String() string     { return strings.Join(*c, ",") }
func (c *catalogsFlag) Set(v string) error { *c = append(*c, v); return nil }

func cmdSync(args []string) error {
	fl := flag.NewFlagSet("sync", flag.ContinueOnError)
	var cats catalogsFlag
	fl.Var(&cats, "catalog", "catalog id to sync (repeatable; replaces the stored choice)")
	all := fl.Bool("all", false, "sync all catalogs, also ones added later")
	undeclared := fl.Bool("include-undeclared", false, "also sync songs without a declared license")
	exclude := fl.String("exclude", "", "leave out songs with these license properties: nc, nd, sa (comma-separated; \"\" for none)")
	key := fl.String("key", "", "the feed's public key (ed25519:...) or a file containing it, if not in the address")
	replace := fl.Bool("replace-local", false, "replace locally changed files with the feed's version")
	jobs := fl.Int("jobs", 4, "parallel downloads")
	dirs, exts := targetsFlag{}, targetsFlag{}
	fl.Var(dirs, "target", "folder for packs of another kind: <kind>=<folder> (repeatable; needs --extensions)")
	fl.Var(exts, "extensions", "file extensions allowed for a kind: <kind>=.ini,.png (repeatable)")
	pos, err := parse(fl, args)
	if err != nil || len(pos) < 1 || len(pos) > 2 || *jobs < 1 || *jobs > 32 {
		return errors.New("usage: kfeed sync [<feed address> | -] <library folder> [--catalog id]... [--all] [--include-undeclared] [--key ed25519:...] [--replace-local] [--jobs 4] [--target <kind>=<folder> --extensions <kind>=.a,.b (repeatable)]")
	}
	targets, err := cliTargets(dirs, exts)
	if err != nil {
		return err
	}
	o := syncOpts{lib: pos[len(pos)-1], key: *key, all: *all, replaceLocal: *replace, jobs: *jobs, targets: targets}
	if len(pos) == 2 {
		o.feed = pos[0]
	}
	if o.feed == "-" {
		if o.feed, err = readAddress(); err != nil {
			return err
		}
	}
	if len(cats) > 0 {
		o.catalogs = cats
	}
	fl.Visit(func(f *flag.Flag) {
		if f.Name == "include-undeclared" {
			o.includeUndeclared = undeclared
		}
		if f.Name == "exclude" {
			list := []string{}
			for _, p := range strings.Split(strings.ToLower(*exclude), ",") {
				if p = strings.TrimSpace(p); p != "" {
					list = append(list, p)
				}
			}
			o.exclude = &list
		}
	})
	rep, err := syncLibrary(o, os.Stdout)
	if rep != nil {
		printReport(os.Stdout, rep)
		if err == nil && len(rep.failed) > 0 {
			err = fmt.Errorf("%d problems, see above; run again later to retry", len(rep.failed))
		}
	}
	return err
}

// readAddress reads the feed address from standard input, so that a personal address (SPEC 5.5) does not end up in
// the shell history.
func readAddress() (string, error) {
	if f, ok := stdin.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			fmt.Fprint(os.Stderr, "Feed address: ")
		}
	}
	line, err := bufio.NewReader(io.LimitReader(stdin, 4096)).ReadString('\n')
	if line = strings.TrimSpace(line); line == "" {
		if err == nil || err == io.EOF {
			err = errors.New("empty")
		}
		return "", fmt.Errorf("no feed address on standard input: %v", err)
	}
	return line, nil
}

// accessNote explains the answers a server gives when it refuses a personal address (SPEC 5.5).
func accessNote(err error) string {
	if s := err.Error(); s == "HTTP 401" || s == "HTTP 403" {
		return s + ", access refused (if this is a private feed: your access may have ended, or your address was replaced)"
	}
	return err.Error()
}

func loadState(dir string) (*syncState, error) {
	st := &syncState{Songs: map[string]*songRec{}, Items: map[string]*itemRec{}}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, fmt.Errorf("%s/state.json is damaged: %v", dir, err)
	}
	if st.Songs == nil {
		st.Songs = map[string]*songRec{}
	}
	if st.Items == nil {
		st.Items = map[string]*itemRec{}
	}
	return st, nil
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// syncPass holds what the first steps of a sync found out. syncLibrary and the API (api.go) share these steps.
type syncPass struct {
	o          syncOpts
	sdir       string
	st         *syncState
	rep        *syncReport
	feed       *Feed
	signed     bool
	locs       []source
	selected   func(id string) bool
	wanted     map[string]Song
	songCat    map[string]string
	byChoice   map[string]bool // not synced because of the player's license choice
	failedCats map[string]bool
	moved      map[string]string
	keyGiven   bool            // the caller passed the key (SPEC 5.6 rule 2), not only learned from the state
	items      map[string]Item // wanted items of other kinds -> their catalog and kind below
	itemCat    map[string]string
	itemKind   map[string]string
}

// openRun locks the library and loads its state. The caller must call unlock when done.
func openRun(o syncOpts) (r *syncPass, unlock func(), err error) {
	sdir := filepath.Join(o.lib, stateDirName)
	// The state is private: it holds the feed address, which can contain an access token (SPEC 5.5).
	if err := os.MkdirAll(filepath.Join(sdir, "catalogs"), 0o700); err != nil {
		return nil, nil, err
	}
	os.Chmod(sdir, 0o700) // also for libraries made by older versions
	lock := filepath.Join(sdir, "lock")
	lf, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("another sync of %s is running (or one crashed: then delete %s)", o.lib, lock)
	}
	fmt.Fprintf(lf, "%d\n", os.Getpid())
	lf.Close()
	unlock = func() { os.Remove(lock) }

	st, err := loadState(sdir)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	return &syncPass{o: o, sdir: sdir, st: st, rep: &syncReport{}}, unlock, nil
}

// fetchFeed finds out where the feed is and which keys it must be signed with, and gets its feed.json.
func (r *syncPass) fetchFeed() error {
	st, o := r.st, &r.o
	given, fragKey, _ := strings.Cut(o.feed, "#ed25519=")
	if fragKey != "" && o.key == "" {
		o.key = "ed25519:" + fragKey
	}
	r.keyGiven = o.key != ""
	if o.key != "" {
		var err error
		if o.key, err = publicKeyArg(o.key); err != nil {
			return err
		}
		if len(st.Trust) > 0 && !slices.Contains(st.Trust, o.key) {
			return fmt.Errorf("the key %s is not one of this subscription's keys %v", o.key, st.Trust)
		}
		if len(st.Trust) == 0 && st.Sequence > 0 {
			return errors.New("this library was subscribed to an unsigned feed; to add a key, subscribe again in a new folder")
		}
		if len(st.Trust) == 0 {
			st.Trust = []string{o.key}
		}
	}
	given = strings.TrimSuffix(given, "feed.json")
	if given == "" && len(st.Locations) == 0 {
		return errors.New("the first sync of a library needs the feed address")
	}
	newLocation := given != "" && !slices.Contains(st.Locations, given)
	if newLocation && len(st.Locations) > 0 && len(st.Trust) == 0 {
		return fmt.Errorf("this feed is unsigned, so it can only be synced from %s (SPEC 6.3)", showLoc(st.Locations[0]))
	}

	feed, _, signed, err := acceptFeed(o.ctx, st, given, newLocation, r.sdir, r.rep)
	if err != nil {
		return err
	}
	r.feed, r.signed = feed, signed
	for _, l := range append(slices.Clone(st.Locations), feed.Mirrors...) {
		src := newSource(l)
		src.ctx = o.ctx
		r.locs = append(r.locs, src)
	}
	return nil
}

// choose applies the catalog and license choice of the options. Without any choice it lists the catalogs on w.
func (r *syncPass) choose(w io.Writer) error {
	st, o, feed := r.st, r.o, r.feed
	ids := []string{}
	for _, c := range feed.Catalogs {
		ids = append(ids, c.ID)
	}
	switch {
	case o.all:
		st.Catalogs = []string{"*"}
	case o.catalogs != nil:
		for _, c := range o.catalogs {
			if !slices.Contains(ids, c) {
				return fmt.Errorf("the feed has no catalog %q (it has: %s)", c, strings.Join(ids, ", "))
			}
		}
		st.Catalogs = o.catalogs
	}
	if o.includeUndeclared != nil {
		st.IncludeUndeclared = *o.includeUndeclared
	}
	if o.exclude != nil {
		for _, p := range *o.exclude {
			if p != "nc" && p != "nd" && p != "sa" {
				return fmt.Errorf("--exclude knows nc (NonCommercial), nd (NoDerivatives) and sa (ShareAlike), not %q", p)
			}
		}
		st.Exclude = *o.exclude
	}
	if st.Catalogs == nil {
		fmt.Fprintf(w, "Feed %q (%s) has these catalogs:\n", feed.Name, feed.Operator.Name)
		for _, c := range feed.Catalogs {
			if c.Kind != KindSongs {
				fmt.Fprintf(w, "  %-24s %-30s %5d items %9.1f MB (kind %s: needs --target and --extensions)\n", c.ID, c.Names["en"], c.Items, float64(c.Bytes)/1e6, c.Kind)
				continue
			}
			fmt.Fprintf(w, "  %-24s %-30s %5d songs %9.1f MB\n", c.ID, c.Names["en"], c.Songs, float64(c.Bytes)/1e6)
		}
		fmt.Fprintln(w, "Choose with --catalog <id> (repeatable) or --all, then run again.")
		return errChooseCatalogs
	}
	r.selected = func(id string) bool { return slices.Contains(st.Catalogs, "*") || slices.Contains(st.Catalogs, id) }
	return nil
}

// loadCatalogs gets the selected catalogs, from the local cache or downloaded, always verified, and decides which
// songs are wanted.
func (r *syncPass) loadCatalogs() {
	st, rep, sdir := r.st, r.rep, r.sdir
	wanted := map[string]Song{}
	songCat := map[string]string{}
	filtered := map[string]bool{} // in a selected catalog, but not synced (unsafe, or the player's license choice)
	byChoice := map[string]bool{}
	failedCats := map[string]bool{}
	moved := map[string]string{}
	keepCache := map[string]bool{}
	items, itemCat, itemKind := map[string]Item{}, map[string]string{}, map[string]string{}
	for _, ref := range r.feed.Catalogs {
		if !r.selected(ref.ID) {
			continue
		}
		var target Target
		if ref.Kind != KindSongs {
			var ok bool
			if target, ok = r.o.targets[ref.Kind]; !ok || !kindRe.MatchString(ref.Kind) {
				failedCats[ref.ID] = true // left alone: its installed items stay as they are
				rep.add(&rep.skippedPacks, fmt.Sprintf("%s: no folder for kind %q", ref.ID, ref.Kind))
				continue
			}
			if !r.signed || !r.keyGiven {
				failedCats[ref.ID] = true
				rep.add(&rep.skippedPacks, fmt.Sprintf("%s: a pack of kind %q needs a signed feed and its key", ref.ID, ref.Kind))
				continue
			}
		}
		name := filepath.Base(ref.Path)
		keepCache[name] = true
		cat, err := loadCatalog(r.locs, ref, filepath.Join(sdir, "catalogs", name))
		if err != nil {
			failedCats[ref.ID] = true
			rep.add(&rep.failed, fmt.Sprintf("catalog %s: %v", ref.ID, err))
			continue
		}
		if ref.Kind != KindSongs {
			for _, it := range cat.Items {
				_, song := wanted[it.ID]
				if _, dup := items[it.ID]; dup || song || filtered[it.ID] {
					continue // first catalog wins (SPEC 3)
				}
				if p := itemProblem(it, target.Extensions); p != "" {
					filtered[it.ID] = true
					rep.add(&rep.unsafe, fmt.Sprintf("%s: %s", it.ID, p))
					continue
				}
				items[it.ID], itemCat[it.ID], itemKind[it.ID] = it, ref.ID, ref.Kind
			}
			continue
		}
		for old, nu := range cat.Moved {
			moved[old] = nu
		}
		for _, s := range cat.Songs {
			if _, item := items[s.ID]; item {
				continue
			}
			if _, dup := wanted[s.ID]; dup || filtered[s.ID] {
				continue // first catalog wins (SPEC 3)
			}
			if p := songProblem(s); p != "" {
				filtered[s.ID] = true
				rep.add(&rep.unsafe, fmt.Sprintf("%s: %s", s.ID, p))
				continue
			}
			if !st.IncludeUndeclared && undeclared(s) {
				filtered[s.ID], byChoice[s.ID] = true, true
				rep.add(&rep.undeclared, s.ID)
				continue
			}
			if p := licenseExcluded(s, st.Exclude); p != "" {
				filtered[s.ID], byChoice[s.ID] = true, true
				rep.add(&rep.excluded, s.ID+" ("+p+")")
				continue
			}
			wanted[s.ID], songCat[s.ID] = s, ref.ID
		}
	}
	if entries, err := os.ReadDir(filepath.Join(sdir, "catalogs")); err == nil {
		for _, e := range entries {
			if !keepCache[e.Name()] {
				os.Remove(filepath.Join(sdir, "catalogs", e.Name()))
			}
		}
	}
	r.wanted, r.songCat, r.byChoice, r.failedCats, r.moved = wanted, songCat, byChoice, failedCats, moved
	r.items, r.itemCat, r.itemKind = items, itemCat, itemKind
}

func syncLibrary(o syncOpts, w io.Writer) (*syncReport, error) {
	// 1. and 2. where the feed is, its keys, feed.json
	r, unlock, err := openRun(o)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := r.fetchFeed(); err != nil {
		return nil, err
	}
	// 3. which catalogs
	if err := r.choose(w); err != nil {
		return nil, err
	}
	// 4. catalogs
	r.loadCatalogs()
	o, st, rep, feed, signed, locs, selected := r.o, r.st, r.rep, r.feed, r.signed, r.locs, r.selected
	wanted, songCat, byChoice, failedCats, moved := r.wanted, r.songCat, r.byChoice, r.failedCats, r.moved

	// 5. renames, withdrawals, songs that are no longer offered
	for old, nu := range moved {
		rec, ok := st.Songs[old]
		if !ok || st.Songs[nu] != nil || !idRe.MatchString(nu) || !idRe.MatchString(old) {
			continue
		}
		if err := os.Rename(filepath.Join(o.lib, old), filepath.Join(o.lib, nu)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			rep.add(&rep.failed, fmt.Sprintf("%s: rename to %s: %v", old, nu, err))
			continue
		}
		st.Songs[nu] = rec
		delete(st.Songs, old)
		rep.add(&rep.moved, old+" → "+nu)
	}
	for id, rec := range st.Songs {
		if _, ok := wanted[id]; ok || failedCats[rec.Catalog] {
			continue
		}
		folder := filepath.Join(o.lib, id)
		switch {
		case signed && slices.Contains(feed.Removed, id):
			removeSong(folder, rec, rep)
			delete(st.Songs, id)
			rep.add(&rep.withdrawn, id)
		case !selected(rec.Catalog) || byChoice[id]:
			removeSong(folder, rec, rep)
			delete(st.Songs, id)
			rep.add(&rep.removed, id)
		case !rec.Hidden:
			if err := hideTxt(folder, rec.Txt); err != nil {
				rep.add(&rep.failed, fmt.Sprintf("%s: hide: %v", id, err))
				continue
			}
			rec.Hidden = true
			rep.add(&rep.hidden, id)
		}
	}
	for id, rec := range st.Items { // SPEC 5.6 rule 6: an item no longer listed keeps its files
		if _, ok := r.items[id]; ok || failedCats[rec.Catalog] {
			continue
		}
		switch {
		case signed && slices.Contains(feed.Removed, id):
			removeItem(id, rec, rep)
			delete(st.Items, id)
			rep.add(&rep.withdrawn, id)
		case !selected(rec.Catalog):
			removeItem(id, rec, rep)
			delete(st.Items, id)
			rep.add(&rep.removed, id)
		}
	}

	// 6. bring every wanted song up to date
	known := map[string]string{} // blob hex -> a verified local file with it
	for id, rec := range st.Songs {
		for name, f := range rec.Files {
			known[f.SHA256] = filepath.Join(o.lib, id, name)
		}
	}
	for _, rec := range st.Items {
		for name, f := range rec.Files {
			if safeItemPath(name) {
				known[f.SHA256] = filepath.Join(rec.Dir, filepath.FromSlash(name))
			}
		}
	}
	plans := []*songPlan{}
	sortedIDs := make([]string, 0, len(wanted))
	for id := range wanted {
		sortedIDs = append(sortedIDs, id)
	}
	sort.Strings(sortedIDs)
	for _, id := range sortedIDs {
		if p := planSong(o, st, id, wanted[id], songCat[id], rep); p != nil {
			plans = append(plans, p)
		}
	}
	claimed := map[string]string{}
	for _, id := range sortedKeys(r.items) {
		if p := planItem(o, st, id, r.items[id], r.itemCat[id], o.targets[r.itemKind[id]].Dir, claimed, rep); p != nil {
			plans = append(plans, p)
		}
	}
	o.progress.start(plans)
	stop := o.progress.every(500 * time.Millisecond)
	seeds := indexSeeds(o.context(), o.seeds, o.lib)
	runDownloads(o, locs, plans, known, seeds, rep, false) // media first
	runDownloads(o, locs, plans, known, seeds, rep, true)  // then the txt, so a song only appears when it is complete
	stop()
	o.progress.finishing()
	for _, p := range plans {
		if p.item != nil {
			finishItem(st, p, rep)
		} else {
			finishSong(o, st, p, rep)
		}
	}

	// 7. save
	data, _ := json.MarshalIndent(st, "", " ")
	if err := writeAtomic(filepath.Join(r.sdir, "state.json"), data); err != nil {
		return rep, err
	}
	return rep, nil
}

// acceptFeed applies SPEC 5.1 and 6. It returns the feed.json to use: a newly accepted one or the cached one.
func acceptFeed(ctx context.Context, st *syncState, given string, newLocation bool, sdir string, rep *syncReport) (*Feed, []byte, bool, error) {
	cached := filepath.Join(sdir, "feed.json")
	try := slices.Clone(st.Locations)
	if given != "" {
		try = append([]string{given}, slices.DeleteFunc(try, func(l string) bool { return l == given })...)
	}
	var notes []string
	for hop := 0; hop < 2; hop++ { // the second round follows moved_to once
		for _, loc := range try {
			src := newSource(loc)
			src.ctx = ctx
			raw, _, err := src.get("feed.json", maxFeedSize)
			if err != nil {
				notes = append(notes, fmt.Sprintf("%s: %v", showLoc(loc), accessNote(err)))
				continue
			}
			var f Feed
			if err := json.Unmarshal(raw, &f); err != nil || f.Spec != Spec {
				notes = append(notes, fmt.Sprintf("%s: not a %s feed", showLoc(loc), Spec))
				continue
			}
			trust, tofu := st.Trust, len(st.Trust) == 0 && st.Sequence == 0
			if tofu && len(f.Keys) > 0 {
				trust = f.Keys // trust on first use
			}
			if len(trust) > 0 {
				sig, _, err := src.get("sig/"+hashBytes(raw)+".sig", 1024)
				if err != nil || len(f.Keys) == 0 || len(f.Keys) > 8 || !sigValid(trust, raw, string(sig)) {
					notes = append(notes, fmt.Sprintf("%s: signature missing or not valid for this subscription's keys", showLoc(loc)))
					continue
				}
			}
			if loc == given && newLocation && !slices.Contains(st.Locations, loc) {
				st.Locations = append([]string{loc}, st.Locations...) // verified: a new place for this feed (LAN, USB)
			}
			if st.Sequence > 0 && (f.Sequence < st.Sequence || (f.Sequence == st.Sequence && hashBytes(raw) != st.FeedSHA)) {
				notes = append(notes, fmt.Sprintf("%s: older than the feed this library already has; its files are still used", showLoc(loc)))
				continue
			}
			if len(trust) > 0 && f.MovedTo != "" && hop == 0 {
				if err := checkMirror(f.MovedTo); err == nil {
					try = []string{strings.TrimSuffix(f.MovedTo, "feed.json")}
					rep.feedNote = "the feed moved to " + try[0]
					st.Locations = append(try, slices.DeleteFunc(st.Locations, func(l string) bool { return l == try[0] })...)
					break
				}
			}
			if len(trust) > 0 {
				st.Trust = f.Keys
			}
			st.Sequence, st.FeedSHA = f.Sequence, hashBytes(raw)
			if len(st.Locations) == 0 {
				st.Locations = []string{loc}
			}
			if err := writeAtomic(cached, raw); err != nil {
				return nil, nil, false, err
			}
			if rep.feedNote == "" {
				rep.feedNote = fmt.Sprintf("feed %q, sequence %d, %s", f.Name, f.Sequence, map[bool]string{true: "signed", false: "unsigned"}[len(trust) > 0])
			}
			return &f, raw, len(trust) > 0, nil
		}
		if !strings.HasPrefix(rep.feedNote, "the feed moved") {
			break
		}
	}
	raw, err := os.ReadFile(cached)
	if err != nil {
		return nil, nil, false, fmt.Errorf("no usable feed.json: %s", strings.Join(notes, "; "))
	}
	var f Feed
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, nil, false, err
	}
	rep.feedNote = "using the feed from the last sync (" + strings.Join(notes, "; ") + ")"
	return &f, raw, len(st.Trust) > 0, nil
}

func loadCatalog(locs []source, ref CatalogRef, cache string) (*Catalog, error) {
	if !hashRe.MatchString(ref.SHA256) || ref.Size < 0 || ref.Size > maxCatalog {
		return nil, errors.New("malformed reference in feed.json")
	}
	if data, err := os.ReadFile(cache); err == nil {
		if c, err := decodeCatalog(ref, data); err == nil {
			return c, nil
		}
	}
	var last error
	for _, s := range locs {
		for try := 1; try <= attempts; try++ {
			data, _, err := s.get(ref.Path, ref.Size)
			if err != nil && len(data) == 0 {
				last = err
				break
			}
			c, err := decodeCatalog(ref, data)
			if err != nil {
				last = err
				continue // incomplete or damaged: try again
			}
			writeAtomic(cache, data)
			return c, nil
		}
	}
	return nil, last
}

// songProblem implements the per-entry checks of SPEC 4.1 and 5.2.
func songProblem(s Song) string {
	if !idRe.MatchString(s.ID) {
		return "invalid id"
	}
	txts, seen := 0, map[string]bool{}
	for _, f := range s.Files {
		if _, known := roleExt[f.Role]; !known {
			continue // unknown roles are ignored
		}
		if p := nameProblem(f.Name, f.Role); p != "" {
			return fmt.Sprintf("file name %q %s", f.Name, p)
		}
		if seen[strings.ToLower(f.Name)] {
			return fmt.Sprintf("two files named %q", f.Name)
		}
		seen[strings.ToLower(f.Name)] = true
		if !hashRe.MatchString(f.SHA256) || f.Size < 0 {
			return "malformed hash or size"
		}
		if f.Role == "txt" {
			txts++
		}
	}
	if txts != 1 {
		return fmt.Sprintf("%d txt files instead of 1", txts)
	}
	return ""
}

func undeclared(s Song) bool {
	for _, f := range s.Files {
		if _, known := roleExt[f.Role]; known && f.Role != "license" && (f.License == nil || f.License.SPDX == "NOLICENSE") {
			return true
		}
	}
	return false
}

// licenseExcluded returns the first license property of the song that the player excluded, or "".
// Creative Commons SPDX ids carry them in their name: CC-BY-NC-SA-4.0 is NonCommercial and ShareAlike.
func licenseExcluded(s Song, exclude []string) string {
	for _, f := range s.Files {
		if _, known := roleExt[f.Role]; !known || f.Role == "license" || f.License == nil {
			continue
		}
		id := strings.ToUpper(f.License.SPDX)
		if !strings.HasPrefix(id, "CC-") {
			continue
		}
		for _, p := range exclude {
			if strings.Contains(id, "-"+strings.ToUpper(p)+"-") {
				return p + ": " + f.License.SPDX
			}
		}
	}
	return ""
}

// localHash returns the hash of a file in the library, using the recorded size and time to skip re-hashing.
// A symlink or anything that is not a regular file counts as foreign ("x").
func localHash(path string, rec fileRec, hasRec bool) (string, bool) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", false
	}
	if !fi.Mode().IsRegular() {
		return "x", true
	}
	if hasRec && fi.Size() == rec.Size && fi.ModTime().UnixNano() == rec.MTime {
		return rec.SHA256, true
	}
	sum, _, err := hashFile(path)
	if err != nil {
		return "x", true
	}
	return sum, true
}

func record(path, sum string) fileRec {
	fi, err := os.Stat(path)
	if err != nil {
		return fileRec{SHA256: sum}
	}
	return fileRec{SHA256: sum, Size: fi.Size(), MTime: fi.ModTime().UnixNano()}
}

type fileJob struct {
	name, hex, path string
	size            int64
	txt             bool
	done            bool
	plan            *songPlan
}

type songPlan struct {
	id, folder, catalog string
	song                Song
	isNew, wasHidden    bool
	jobs                []*fileJob
	keep                map[string]fileRec // files that are fine as they are, or kept local versions
	failed              bool
	left                int   // jobs not processed yet; guarded by the tracker
	item                *Item // set for an item of another kind (items.go); song then only holds its name
}

// fileAction decides what a sync does with one file of a wanted song: keep the local file (keep is its record, and
// local is true for a version changed here, SPEC 5.2) or download it.
func fileAction(o syncOpts, path string, f File, rec *songRec) (keep fileRec, local, download bool) {
	want := strings.TrimPrefix(f.SHA256, "sha256:")
	old, hasRec := rec.Files[f.Name]
	have, exists := localHash(path, old, hasRec)
	switch {
	case exists && have == want:
		return record(path, want), false, false
	case exists && !o.replaceLocal && (!hasRec || have != old.SHA256):
		return record(path, have), true, false // changed here, or not ours: never overwrite without asking
	}
	return fileRec{}, false, true
}

func planSong(o syncOpts, st *syncState, id string, s Song, catalog string, rep *syncReport) *songPlan {
	folder := filepath.Join(o.lib, id)
	if fi, err := os.Lstat(folder); err == nil && !fi.IsDir() {
		rep.add(&rep.failed, fmt.Sprintf("%s: %s is not a folder", id, folder))
		return nil
	}
	rec := st.Songs[id]
	p := &songPlan{id: id, folder: folder, catalog: catalog, song: s, isNew: rec == nil, keep: map[string]fileRec{}}
	if rec == nil {
		rec = &songRec{Files: map[string]fileRec{}}
	}
	if rec.Hidden {
		visible := filepath.Join(folder, rec.Txt)
		if _, err := os.Lstat(visible); errors.Is(err, fs.ErrNotExist) {
			os.Rename(visible+hiddenSuffix, visible)
		}
		p.wasHidden = true
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		rep.add(&rep.failed, fmt.Sprintf("%s: %v", id, err))
		return nil
	}
	for _, f := range s.Files {
		if _, known := roleExt[f.Role]; !known {
			continue
		}
		path := filepath.Join(folder, f.Name)
		keep, local, download := fileAction(o, path, f, rec)
		if download {
			want := strings.TrimPrefix(f.SHA256, "sha256:")
			p.jobs = append(p.jobs, &fileJob{name: f.Name, hex: want, path: path, size: f.Size, txt: f.Role == "txt", plan: p})
			continue
		}
		if local {
			// Changed here, or not written by us: kept. Its record stays what we wrote (or none for a foreign file),
			// so that removing the song later still sees the change and keeps the file (SPEC 5.2).
			if old, ours := rec.Files[f.Name]; ours {
				p.keep[f.Name] = old
			}
			rep.add(&rep.keptLocal, id+"/"+f.Name)
			continue
		}
		p.keep[f.Name] = keep
	}
	return p
}

func runDownloads(o syncOpts, locs []source, plans []*songPlan, known map[string]string, seeds *seedIndex, rep *syncReport, txt bool) {
	var mu sync.Mutex
	queue := make(chan *fileJob)
	var wg sync.WaitGroup
	for i := 0; i < o.jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				if o.ctx != nil && o.ctx.Err() != nil {
					continue // cancelled: the file stays as it is, so the song stays hidden or old (finishSong)
				}
				o.progress.working(j.plan.song)
				mu.Lock()
				src := known[j.hex]
				mu.Unlock()
				err := errors.New("no copy")
				if src != "" && src != j.path {
					if err = copyVerified(src, j.path, j.hex, j.size); err == nil {
						o.progress.addBytes(j.size)
					}
				}
				if err != nil {
					if seed := seeds.find(o.context(), j.hex, j.size); seed != "" {
						if err = takeSeed(o.context(), seed, j.path, j.hex, j.size, j.txt); err == nil {
							o.progress.addBytes(j.size)
							rep.mu.Lock()
							rep.imported += j.size
							rep.mu.Unlock()
						}
					}
				}
				if err != nil {
					var n int64
					var retries int
					n, retries, err = fetchBlob(locs, j.hex, j.size, j.path, o.progress)
					rep.mu.Lock()
					rep.downloaded += n
					rep.retried += retries
					rep.mu.Unlock()
				}
				if err != nil {
					if o.ctx == nil || o.ctx.Err() == nil {
						rep.add(&rep.failed, fmt.Sprintf("%s: %v", j.path, err))
					}
					o.progress.processed(j)
					continue
				}
				j.done = true
				o.progress.processed(j)
				mu.Lock()
				known[j.hex] = j.path
				mu.Unlock()
			}
		}()
	}
	for _, p := range plans {
		if txt && slices.ContainsFunc(p.jobs, func(j *fileJob) bool { return !j.txt && !j.done }) {
			p.failed = true // media incomplete: don't make the song visible
			continue
		}
		for _, j := range p.jobs {
			if j.txt == txt {
				queue <- j
			}
		}
	}
	close(queue)
	wg.Wait()
}

// attempts is how often a file is downloaded from one location before the next location is tried. CDNs have been
// seen to end large downloads early without any error, so an incomplete or wrong file gets two more chances.
const attempts = 3

// fetchBlob downloads one file into place, trying every location; it is renamed into place only when verified.
// It returns the bytes transferred and how many attempts had to be repeated. An interrupted download stays as a part
// file named after its hash (partPath), so that a later attempt or sync continues it instead of starting again.
func fetchBlob(locs []source, hexsum string, size int64, dest string, t *tracker) (int64, int, error) {
	rel := "blobs/" + hexsum[:2] + "/" + hexsum
	part := partPath(dest, hexsum)
	if _, busy := activeParts.LoadOrStore(part, true); busy {
		part = "" // another job downloads the same file into this folder: this one doesn't keep a part file
	} else {
		defer activeParts.Delete(part)
	}
	var last error = errors.New("no location")
	var total int64
	retries := 0
	for _, s := range locs {
		for try := 1; try <= attempts; try++ {
			if try > 1 {
				retries++
			}
			n, next, err := fetchOnce(s, rel, hexsum, size, dest, part, t)
			total += n
			if err == nil {
				return total, retries, nil
			}
			last = err
			if next {
				break // not there, or refused: next location (open already waits on 429/503)
			}
		}
	}
	return total, retries, fmt.Errorf("%v (after %d attempts per location)", last, attempts)
}

// partPath is where the download of the file with this hash into dest's folder is kept until it is complete.
func partPath(dest, hexsum string) string {
	return filepath.Join(filepath.Dir(dest), partPrefix+hexsum)
}

// activeParts holds the part files that a job is writing (path -> true).
var activeParts sync.Map

// fetchOnce makes one download attempt. With part != "" it continues the bytes already in the part file (Range
// request) and keeps them when the attempt breaks off. next is true when this location can't deliver the file.
func fetchOnce(s source, rel, hexsum string, size int64, dest, part string, t *tracker) (n int64, next bool, err error) {
	var f *os.File
	if part != "" {
		f, err = os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o644)
	} else {
		f, err = os.CreateTemp(filepath.Dir(dest), partPrefix+"*")
	}
	if err != nil {
		return 0, false, err
	}
	tmp := f.Name()
	h := sha256.New()
	have, err := io.Copy(h, f) // what an earlier attempt left; the file offset is now at its end
	if err != nil || have >= size {
		have, err = 0, restart(f, h)
		if err != nil {
			f.Close()
			return 0, false, err
		}
	}
	r, from, err := s.openFrom(rel, have)
	if err != nil {
		f.Close()
		if part == "" {
			os.Remove(tmp)
		}
		return 0, true, err
	}
	if from != have { // the server sent the whole file
		have = 0
		if err := restart(f, h); err != nil {
			r.Close()
			f.Close()
			return 0, false, err
		}
	}
	n, err = io.Copy(io.MultiWriter(f, h, t.counter()), io.LimitReader(r, size-have+1)) // never more than declared (SPEC 5.1.5)
	r.Close()
	cerr := f.Close()
	got := have + n
	keep := false // a part file with a correct beginning is kept for the next attempt
	switch {
	case err != nil || cerr != nil:
		err = fmt.Errorf("%s: download broke off after %d of %d bytes: %v", showLoc(s.base), got, size, errors.Join(err, cerr))
		keep = true
	case got > size:
		err = fmt.Errorf("%s: larger than the %d bytes the catalog says", showLoc(s.base), size)
	case got < size:
		err = fmt.Errorf("%s: got %d of %d bytes (the download ended early)", showLoc(s.base), got, size)
		keep = true
	case hex.EncodeToString(h.Sum(nil)) != hexsum:
		err = fmt.Errorf("%s: content does not match its hash", showLoc(s.base))
	default:
		return n, false, os.Rename(tmp, dest)
	}
	if !keep || part == "" || got == 0 {
		os.Remove(tmp)
	}
	return n, false, err
}

// restart empties a part file and its running hash.
func restart(f *os.File, h hash.Hash) error {
	h.Reset()
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.Seek(0, io.SeekStart)
	return err
}

// removeParts deletes the part files in a song folder (after the song is complete, or removed).
func removeParts(folder string) {
	entries, _ := os.ReadDir(folder)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), partPrefix) && !e.IsDir() {
			os.Remove(filepath.Join(folder, e.Name()))
		}
	}
}

func copyVerified(src, dest, hexsum string, size int64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(dest), partPrefix+"*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, size+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && (n != size || hex.EncodeToString(h.Sum(nil)) != hexsum) {
		err = errors.New("local copy changed")
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

func finishSong(o syncOpts, st *syncState, p *songPlan, rep *syncReport) {
	rec := st.Songs[p.id]
	if rec == nil {
		rec = &songRec{Files: map[string]fileRec{}}
	}
	txt := ""
	files := map[string]fileRec{}
	for name, f := range p.keep {
		files[name] = f
	}
	for _, j := range p.jobs {
		if j.done {
			files[j.name] = record(j.path, j.hex)
		} else if old, ok := rec.Files[j.name]; ok {
			files[j.name] = old // update failed: the old file stays
		}
		if j.txt {
			txt = j.name
		}
	}
	for _, f := range p.song.Files {
		if f.Role == "txt" {
			txt = f.Name
		}
	}
	if p.failed || slices.ContainsFunc(p.jobs, func(j *fileJob) bool { return !j.done }) {
		if p.isNew {
			hideTxt(p.folder, txt) // nothing of it is visible yet; keep it that way
		}
		st.Songs[p.id] = &songRec{Catalog: p.catalog, Txt: txt, Hidden: p.isNew || rec.Hidden, Files: files}
		return
	}
	// files the song no longer has: delete them unless changed here
	for name, old := range rec.Files {
		if _, still := files[name]; still {
			continue
		}
		path := filepath.Join(p.folder, name)
		if have, ok := localHash(path, old, true); ok && have == old.SHA256 {
			os.Remove(path)
		}
	}
	st.Songs[p.id] = &songRec{Catalog: p.catalog, Txt: txt, Files: files}
	removeParts(p.folder) // left over from files the song no longer has
	switch {
	case p.isNew:
		rep.add(&rep.added, p.id)
	case p.wasHidden:
		rep.add(&rep.unhidden, p.id)
	case len(p.jobs) > 0:
		rep.add(&rep.updated, p.id)
	default:
		rep.add(&rep.unchanged, p.id)
	}
}

func hideTxt(folder, txt string) error {
	if txt == "" {
		return nil
	}
	path := filepath.Join(folder, txt)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return os.Rename(path, path+hiddenSuffix)
}

// removeSong deletes a song's files, except files changed here; their txt is hidden so the game ignores them.
func removeSong(folder string, rec *songRec, rep *syncReport) {
	for name, old := range rec.Files {
		path := filepath.Join(folder, name)
		if rec.Hidden && name == rec.Txt {
			path += hiddenSuffix
		}
		have, ok := localHash(path, old, true)
		switch {
		case !ok:
		case have == old.SHA256:
			os.Remove(path)
		default:
			rep.add(&rep.keptLocal, filepath.Base(folder)+"/"+name)
			if name == rec.Txt && !rec.Hidden {
				os.Rename(path, path+hiddenSuffix)
			}
		}
	}
	removeParts(folder) // interrupted downloads of this song
	os.Remove(folder)   // only succeeds if empty
}

func printReport(w io.Writer, r *syncReport) {
	fmt.Fprintln(w, r.feedNote)
	for _, l := range []struct {
		what string
		ids  []string
	}{{"added", r.added}, {"updated", r.updated}, {"renamed", r.moved}, {"shown again", r.unhidden},
		{"hidden (no longer in the feed; files kept)", r.hidden}, {"withdrawn by the operator (deleted)", r.withdrawn},
		{"removed (catalog or license no longer chosen)", r.removed}} {
		if len(l.ids) > 0 {
			fmt.Fprintf(w, "  %s (%d): %s\n", l.what, len(l.ids), list(slices.Clone(l.ids)))
		}
	}
	fmt.Fprintf(w, "  unchanged: %d songs; downloaded %.1f MB\n", len(r.unchanged), float64(r.downloaded)/1e6)
	if r.retried > 0 {
		fmt.Fprintf(w, "Note: %d downloads arrived incomplete or damaged and were repeated. If this happens often, the server or CDN cuts off downloads.\n", r.retried)
	}
	if len(r.undeclared) > 0 {
		fmt.Fprintf(w, "Not synced, no license declared (%d): %s\n  → --include-undeclared to sync them anyway\n", len(r.undeclared), list(slices.Clone(r.undeclared)))
	}
	if len(r.excluded) > 0 {
		fmt.Fprintf(w, "Not synced, excluded by your license choice (%d): %s\n  → --exclude \"\" to sync them again\n", len(r.excluded), list(slices.Clone(r.excluded)))
	}
	if len(r.keptLocal) > 0 {
		fmt.Fprintf(w, "Changed in your library, kept as they are (%d): %s\n  → --replace-local to take the feed's version\n", len(r.keptLocal), list(slices.Clone(r.keptLocal)))
	}
	for _, s := range r.unsafe {
		fmt.Fprintln(w, "  skipped, unsafe entry:", s)
	}
	for _, s := range r.skippedPacks {
		fmt.Fprintln(w, "  skipped pack:", s)
	}
	for _, s := range r.failed {
		fmt.Fprintln(w, "  ! ", s)
	}
}
