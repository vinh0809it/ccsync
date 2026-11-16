package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vinh0809it/ccsync/internal/testutil"
)

func TestRegistry(t *testing.T) {
	testutil.NewMachine(t)
	reg, err := LoadRegistry()
	if err != nil || len(reg.Stores) != 0 {
		t.Fatalf("fresh registry: %+v %v", reg, err)
	}
	if _, err := reg.Get(""); err != ErrNoStore {
		t.Fatalf("Get on empty registry: %v", err)
	}

	reg.Put(Entry{Name: "work", RepoURL: "w", Dir: "/w"})
	reg.Put(Entry{Name: "home", RepoURL: "h", Dir: "/h"})
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}
	reg, _ = LoadRegistry()
	if e, err := reg.Get(""); err != nil || e.Name != "home" {
		t.Fatalf("current store = %+v %v, want the last added", e, err)
	}
	if err := reg.Use("work"); err != nil {
		t.Fatal(err)
	}
	if e, _ := reg.Get(""); e.Name != "work" {
		t.Fatalf("after Use: %+v", e)
	}
	if _, err := reg.Get("nope"); err == nil {
		t.Fatal("unknown store accepted")
	}
	if _, err := reg.Remove("work"); err != nil {
		t.Fatal(err)
	}
	// One store left: it becomes current.
	if e, err := reg.Get(""); err != nil || e.Name != "home" {
		t.Fatalf("after removing the current store: %+v %v", e, err)
	}

	for _, bad := range []string{"", "Work", "../x", "a b", strings.Repeat("x", 33), "-x"} {
		if CheckName(bad) == nil {
			t.Errorf("CheckName(%q) accepted", bad)
		}
	}
}

// TestLegacyConfig: a machine set up by ccsync 0.1 keeps working.
func TestLegacyConfig(t *testing.T) {
	m := testutil.NewMachine(t)
	os.MkdirAll(m.Config, 0o700)
	os.WriteFile(filepath.Join(m.Config, "config.json"), []byte(`{"repo_url":"git@x:me/s.git"}`), 0o600)
	reg, err := LoadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	e, err := reg.Get("")
	if err != nil || e.Name != "default" || e.RepoURL != "git@x:me/s.git" || e.Dir != filepath.Join(m.Data, "store") {
		t.Fatalf("legacy entry = %+v %v", e, err)
	}
}

// TestDeleteOnlyRemovesClones: forgetting a store must never delete a
// directory that isn't its clone.
func TestForgetOnlyRemovesClones(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "precious.txt"), []byte("x"), 0o644)
	if err := Open(Entry{Dir: dir}).Forget(); err == nil {
		t.Fatal("deleted a directory that isn't a store clone")
	}
	if _, err := os.Stat(filepath.Join(dir, "precious.txt")); err != nil {
		t.Fatal("file removed")
	}

	st := machine(t)
	open(t, st, testutil.Remote(t))
	if err := st.Forget(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.Dir); !os.IsNotExist(err) {
		t.Fatal("clone still there")
	}
}
