package kfeed

// API for graphical clients such as launchers. It uses the same code as "kfeed sync" and prints nothing.
// In this API a catalog is called a pack.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Info describes a feed before anything is downloaded.
type Info struct {
	Name, Description, Homepage, OperatorName, OperatorContact string
	Signed                                                     bool
	// Subscribed is true once packs were chosen for this library, even if the choice is now "none".
	Subscribed bool
	Packs      []Pack
}

// Pack is one catalog of a feed.
type Pack struct {
	ID     string
	Names  map[string]string // language -> name
	Songs  int
	Bytes  int64
	Chosen bool // currently selected in this library
}

// Options say which feed and library to work on.
type Options struct {
	Address string   // feed address; "" = the one stored in the library
	Key     string   // "ed25519:..." or ""
	Library string   // folder for this feed
	Packs   []string // nil = keep stored choice; empty = no packs (removes their songs)
	Exclude []string // license properties: "nc", "nd", "sa"; nil = keep stored choice
	Jobs    int      // parallel downloads; 0 = default (4)
	// Seeds are folders with songs the player has already (for example an older installation): files with the same
	// content are taken from there (hard link, or copy) instead of being downloaded. The folders are not changed.
	Seeds []string
}

// Progress is sent often while Sync works; reading it must be cheap.
type Progress struct {
	Phase                 string // "checking", "downloading", "finishing"
	SongsDone, SongsTotal int    // all chosen songs; those that are complete already count as done
	BytesDone, BytesTotal int64  // all files of the chosen songs; files that are there already count as done
	Current               string // song being worked on: "Artist - Title"
}

// Result lists what Sync changed, by song id.
type Result struct {
	Added   []string // new, or visible again
	Updated []string // changed or renamed
	Removed []string // removed, withdrawn by the operator, or hidden because the feed no longer offers them
	Failed  []string // one sentence each; a later Sync retries
	// BytesDownloaded counts the bytes transferred, including repeated attempts.
	BytesDownloaded int64
	// BytesImported counts the bytes taken from the seed folders instead of being downloaded.
	BytesImported int64
}

// ErrNoPacks is returned by Plan and Sync for a library without a stored pack choice when Options.Packs is nil.
var ErrNoPacks = errChooseCatalogs

func (o Options) syncOpts(ctx context.Context) (syncOpts, error) {
	if o.Library == "" {
		return syncOpts{}, errors.New("no library folder")
	}
	so := syncOpts{ctx: ctx, feed: o.Address, key: o.Key, lib: o.Library, catalogs: slices.Clone(o.Packs), jobs: o.Jobs,
		seeds: slices.Clone(o.Seeds)}
	if o.Packs != nil && so.catalogs == nil {
		so.catalogs = []string{}
	}
	if so.jobs == 0 {
		so.jobs = 4
	}
	if so.jobs < 1 || so.jobs > 32 {
		return syncOpts{}, errors.New("jobs must be between 1 and 32")
	}
	if o.Exclude != nil {
		ex := slices.Clone(o.Exclude)
		so.exclude = &ex
	}
	return so, nil
}

// Inspect fetches and verifies the feed description and pack list. It writes nothing into songs; it may update
// the cached feed files in the library's state folder.
func Inspect(ctx context.Context, o Options) (*Info, error) {
	so, err := o.syncOpts(ctx)
	if err != nil {
		return nil, err
	}
	r, unlock, err := openRun(so)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := r.fetchFeed(); err != nil {
		return nil, err
	}
	f := r.feed
	info := &Info{Name: f.Name, Description: f.Description, Homepage: f.Homepage, OperatorName: f.Operator.Name,
		OperatorContact: f.Operator.Contact, Signed: r.signed, Subscribed: r.st.Catalogs != nil}
	all := slices.Contains(r.st.Catalogs, "*")
	for _, c := range f.Catalogs {
		info.Packs = append(info.Packs, Pack{ID: c.ID, Names: c.Names, Songs: c.Songs, Bytes: c.Bytes,
			Chosen: all || slices.Contains(r.st.Catalogs, c.ID)})
	}
	return info, nil
}

// Plan reports what Sync with the same options would do: the bytes to download (also the free space needed) and
// the number of songs with something to download. It does not save the options. Files in the seed folders are
// recognised by their size only (an estimate: Sync compares the content).
func Plan(ctx context.Context, o Options) (download int64, songs int, err error) {
	so, err := o.syncOpts(ctx)
	if err != nil {
		return 0, 0, err
	}
	r, unlock, err := openRun(so)
	if err != nil {
		return 0, 0, err
	}
	defer unlock()
	if err := r.fetchFeed(); err != nil {
		return 0, 0, err
	}
	if err := r.choose(io.Discard); err != nil {
		return 0, 0, err
	}
	r.loadCatalogs()
	if len(r.rep.failed) > 0 {
		return 0, 0, errors.New(r.rep.failed[0])
	}
	download, songs = r.plan()
	return download, songs, nil
}

// plan counts like step 6 of syncLibrary, without changing anything. A blob that is in the library already, or
// that another song downloads, is not counted twice.
func (r *syncPass) plan() (int64, int) {
	known := map[string]bool{}
	for _, rec := range r.st.Songs {
		for _, f := range rec.Files {
			known[f.SHA256] = true
		}
	}
	seeds := indexSeeds(r.o.context(), r.o.seeds, r.o.lib)
	var bytes int64
	songs := 0
	for id, s := range r.wanted {
		rec := r.st.Songs[id]
		if rec == nil {
			rec = &songRec{}
		}
		need := false
		for _, f := range s.Files {
			if _, ok := roleExt[f.Role]; !ok {
				continue
			}
			if _, _, download := fileAction(r.o, filepath.Join(r.o.lib, id, f.Name), f, rec); !download {
				continue
			}
			if h := strings.TrimPrefix(f.SHA256, "sha256:"); !known[h] {
				known[h] = true
				if seeds.has(f.Size) {
					continue // probably there already: taken from a seed folder
				}
				bytes += f.Size
			}
			need = true
		}
		if need {
			songs++
		}
	}
	return bytes, songs
}

// Sync downloads and updates. It stops soon after ctx is cancelled and leaves a consistent library (a later Sync
// continues); it then returns ctx.Err() together with the result so far. progress may be nil.
func Sync(ctx context.Context, o Options, progress func(Progress)) (*Result, error) {
	so, err := o.syncOpts(ctx)
	if err != nil {
		return nil, err
	}
	if progress != nil {
		so.progress = &tracker{fn: progress, p: Progress{Phase: "checking"}}
		so.progress.send()
	}
	rep, err := syncLibrary(so, io.Discard)
	if rep == nil {
		return nil, err
	}
	res := &Result{BytesDownloaded: rep.downloaded, BytesImported: rep.imported, Failed: slices.Clone(rep.failed)}
	res.Added = slices.Concat(rep.added, rep.unhidden)
	res.Updated = slices.Concat(rep.updated, rep.moved)
	res.Removed = slices.Concat(rep.removed, rep.withdrawn, rep.hidden)
	if err == nil {
		err = ctx.Err()
	}
	return res, err
}

// tracker collects the progress of a sync. All methods may be called on a nil tracker; they then do nothing.
type tracker struct {
	fn func(Progress)
	mu sync.Mutex
	p  Progress
}

func (t *tracker) send() {
	if t == nil {
		return
	}
	t.mu.Lock()
	p := t.p
	t.mu.Unlock()
	t.fn(p)
}

// start sets the totals from the plans of all chosen songs. What is there already counts as done, so that a
// download that continues after a pause doesn't start again at 0 %.
func (t *tracker) start(plans []*songPlan) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.p.Phase = "downloading"
	for _, p := range plans {
		// a song counts as done when its media files are: all txt files come at the very end (sync.go)
		p.left = 0
		for _, j := range p.jobs {
			if !j.txt {
				p.left++
			}
		}
		t.p.SongsTotal++
		if p.left == 0 {
			t.p.SongsDone++
		}
		for _, j := range p.jobs {
			t.p.BytesTotal += j.size
			// the part of an interrupted download is continued (fetchBlob): its bytes count as done
			if fi, err := os.Stat(partPath(j.path, j.hex)); err == nil && fi.Size() < j.size {
				t.p.BytesDone += fi.Size()
			}
		}
		for _, f := range p.keep {
			t.p.BytesTotal += f.Size
			t.p.BytesDone += f.Size
		}
	}
	t.mu.Unlock()
	t.send()
}

// every sends the progress at this interval until the returned stop is called.
func (t *tracker) every(d time.Duration) (stop func()) {
	if t == nil {
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		tick := time.NewTicker(d)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				t.send()
			case <-done:
				return
			}
		}
	}()
	return func() { close(done); <-finished }
}

func (t *tracker) finishing() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.p.Phase, t.p.Current = "finishing", ""
	t.mu.Unlock()
	t.send()
}

func (t *tracker) working(s Song) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.p.Current = s.Artist + " - " + s.Title
	t.mu.Unlock()
}

func (t *tracker) addBytes(n int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.p.BytesDone = min(t.p.BytesDone+n, t.p.BytesTotal) // repeated attempts count again
	t.mu.Unlock()
}

// processed marks one file job of p as done or failed.
func (t *tracker) processed(j *fileJob) {
	if t == nil || j.txt {
		return
	}
	t.mu.Lock()
	if j.plan.left--; j.plan.left == 0 {
		t.p.SongsDone++
	}
	t.mu.Unlock()
}

type countWriter struct{ t *tracker }

func (c countWriter) Write(b []byte) (int, error) {
	c.t.addBytes(int64(len(b)))
	return len(b), nil
}

// counter returns a writer that counts the bytes written as downloaded.
func (t *tracker) counter() io.Writer {
	if t == nil {
		return io.Discard
	}
	return countWriter{t}
}
