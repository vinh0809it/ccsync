package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/vinh0809it/ccsync/internal/testutil"
)

// TestLockSerializesProcesses: two openings of the same store (as the GUI
// and a CLI command would hold) never run at once.
func TestLockSerializesProcesses(t *testing.T) {
	m := testutil.NewMachine(t)
	e := Entry{Name: "s", Dir: filepath.Join(m.Data, "stores", "s")}
	first, second := Open(e), Open(e)

	release, err := first.Lock()
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		unlock, err := second.Lock()
		if err != nil {
			t.Error(err)
		} else {
			unlock()
		}
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("second lock acquired while the first was held")
	case <-time.After(500 * time.Millisecond):
	}
	release()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("second lock not acquired after release")
	}
}
