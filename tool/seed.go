package kfeed

// Seed folders: folders with songs the player has already, for example from an older installation. A file the
// library needs that is there with the same SHA-256 is taken from there instead of being downloaded. The names and
// the layout of the seed folders don't matter, only the content of the files.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// seedIndex lists the files of the seed folders by size; sizes are cheap to compare, hashes are computed only for
// files of a size that is needed.
type seedIndex struct {
	bySize map[int64][]string
}

// indexSeeds walks the seed folders. Symbolic links, hidden folders and the library itself are skipped.
func indexSeeds(ctx context.Context, dirs []string, lib string) *seedIndex {
	if len(dirs) == 0 {
		return nil
	}
	ix := &seedIndex{bySize: map[int64][]string{}}
	libAbs, _ := filepath.Abs(lib)
	for _, d := range dirs {
		filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return filepath.SkipAll
			}
			if err != nil {
				return nil // unreadable: skip it
			}
			if e.IsDir() {
				abs, _ := filepath.Abs(p)
				if p != d && (strings.HasPrefix(e.Name(), ".") || abs == libAbs) {
					return filepath.SkipDir
				}
				return nil
			}
			if !e.Type().IsRegular() {
				return nil
			}
			if fi, err := e.Info(); err == nil && fi.Size() > 0 {
				ix.bySize[fi.Size()] = append(ix.bySize[fi.Size()], p)
			}
			return nil
		})
	}
	return ix
}

// has reports whether a file of this size is in the seed folders: Plan uses it as an estimate, without hashing.
func (ix *seedIndex) has(size int64) bool {
	return ix != nil && len(ix.bySize[size]) > 0
}

// find returns a seed file with this content, or "".
func (ix *seedIndex) find(ctx context.Context, hexsum string, size int64) string {
	if ix == nil {
		return ""
	}
	for _, p := range ix.bySize[size] {
		if ctx.Err() != nil {
			return ""
		}
		if sum, _ := seedHash(ctx, p); sum == hexsum {
			return p
		}
	}
	return ""
}

type seedRec struct {
	size, mtime int64
	sum         string
}

// seedHashes remembers the hashes of seed files for the life of the process (path -> seedRec), so that the files
// of an old installation are read once, not on every sync.
var seedHashes sync.Map

// seedHash returns the hex SHA-256 of a seed file and the record it is based on.
func seedHash(ctx context.Context, p string) (string, seedRec) {
	fi, err := os.Stat(p)
	if err != nil {
		return "", seedRec{}
	}
	if r, ok := seedHashes.Load(p); ok {
		if rec := r.(seedRec); rec.size == fi.Size() && rec.mtime == fi.ModTime().UnixNano() {
			return rec.sum, rec
		}
	}
	f, err := os.Open(p)
	if err != nil {
		return "", seedRec{}
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, ctxReader{ctx, f}); err != nil {
		return "", seedRec{}
	}
	rec := seedRec{size: fi.Size(), mtime: fi.ModTime().UnixNano(), sum: hex.EncodeToString(h.Sum(nil))}
	seedHashes.Store(p, rec)
	return rec.sum, rec
}

// ctxReader stops reading when the context is cancelled: hashing a large video can take a while.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(b)
}

// takeSeed puts a seed file into place. A media file becomes a hard link: no extra disk space, and the seed folder
// stays unchanged. A txt file (games sometimes edit them), or a file on another drive or a file system without hard
// links, is copied and checked.
func takeSeed(ctx context.Context, src, dest, hexsum string, size int64, txt bool) error {
	if !txt {
		if sum, rec := seedHash(ctx, src); sum == hexsum {
			tmp := partPath(dest, hexsum) + "-link"
			os.Remove(tmp)
			if err := os.Link(src, tmp); err == nil {
				fi, err := os.Stat(tmp)
				// unchanged since it was hashed: same size and modification time
				if err == nil && fi.Size() == size && fi.Size() == rec.size && fi.ModTime().UnixNano() == rec.mtime {
					if err := os.Rename(tmp, dest); err == nil {
						return nil
					}
				}
				os.Remove(tmp)
			}
		}
	}
	return copyVerified(src, dest, hexsum, size)
}
