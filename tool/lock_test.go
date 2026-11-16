package kfeed

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRunLock: a lock file left by a crashed sync does not block the next one; two syncs at the same time do.
func TestRunLock(t *testing.T) {
	lib := t.TempDir()
	sdir := filepath.Join(lib, stateDirName)
	if err := os.MkdirAll(sdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdir, "lock"), []byte("999999\n"), 0o600); err != nil { // the crashed run's
		t.Fatal(err)
	}
	_, unlock, err := openRun(syncOpts{lib: lib})
	if err != nil {
		t.Fatalf("after a crash: %v", err)
	}
	if _, unlock2, err := openRun(syncOpts{lib: lib}); err == nil {
		unlock2()
		t.Fatal("a second sync of the same library was allowed")
	}
	unlock()
	_, unlock, err = openRun(syncOpts{lib: lib})
	if err != nil {
		t.Fatalf("after unlock: %v", err)
	}
	unlock()
}
