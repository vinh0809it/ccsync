package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vinh0809it/ccsync/internal/session"
	"github.com/vinh0809it/ccsync/internal/store"
	"github.com/vinh0809it/ccsync/internal/testutil"
)

// TestSeveralStores: one machine, two stores with their own passphrases.
func TestSeveralStores(t *testing.T) {
	m := testutil.NewMachine(t)
	work, home := testutil.Remote(t), testutil.Remote(t)
	stores := Stores{}
	if _, err := stores.Add(NewStore{Name: "work", Location: work}, "work-pass"); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Add(NewStore{Name: "home", Location: home}, "home-pass"); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Add(NewStore{Name: "work", Location: home}, "x"); err == nil {
		t.Fatal("a second repo under an existing name was accepted")
	}

	// Each store opens only with its own passphrase.
	if _, err := stores.Unlock("work", "home-pass"); err == nil {
		t.Fatal("work opened with home's passphrase")
	}
	c, err := stores.Unlock("work", "work-pass")
	if err != nil || c.Entry.Name != "work" {
		t.Fatalf("unlock work: %+v %v", c, err)
	}
	// Unlocking selects it.
	reg, _ := stores.List()
	if reg.Current != "work" {
		t.Fatalf("current = %q", reg.Current)
	}

	s := localSession(t, m, "eeeeeeee-aaaa-bbbb-cccc-000000000001", `{"type":"user","uuid":"u1"}`)
	if _, err := c.Push([]session.Session{s}, "", PushOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(testutil.RemoteFiles(t, work), s.ID) {
		t.Fatal("push did not go to the work store")
	}
	if strings.Contains(testutil.RemoteFiles(t, home), s.ID) {
		t.Fatal("push leaked into the home store")
	}
}

// TestForgottenPassphrase: forget the store on this machine and set up a
// new one — no manual cleanup, local sessions untouched, old repo intact.
func TestForgottenPassphrase(t *testing.T) {
	m := testutil.NewMachine(t)
	old, fresh := testutil.Remote(t), testutil.Remote(t)
	stores := Stores{}
	c, err := stores.Add(NewStore{Name: "main", Location: old}, "lost-pass")
	if err != nil {
		t.Fatal(err)
	}
	s := localSession(t, m, "ffffffff-aaaa-bbbb-cccc-000000000001", `{"type":"user","uuid":"u1"}`)
	if _, err := c.Push([]session.Session{s}, "", PushOptions{}); err != nil {
		t.Fatal(err)
	}
	clone := c.Entry.Dir

	if err := stores.Forget("main"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatal("clone not removed")
	}
	if _, err := os.Stat(s.File); err != nil {
		t.Fatal("forgetting a store touched local sessions")
	}
	if !strings.Contains(testutil.RemoteFiles(t, old), s.ID) {
		t.Fatal("forgetting a store touched its repo")
	}

	// Same name, new repo and passphrase.
	c, err = stores.Add(NewStore{Name: "main", Location: fresh}, "new-pass")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := c.Push([]session.Session{s}, "", PushOptions{}); err != nil || len(res.Pushed) != 1 {
		t.Fatalf("push to the new store: %+v %v", res, err)
	}
}

// TestFailedAddLeavesNothing: a wrong passphrase for an existing store
// leaves no clone or registry entry behind, so the next attempt just works.
func TestFailedAddLeavesNothing(t *testing.T) {
	remote := testutil.Remote(t)
	testutil.NewMachine(t) // another machine creates the store
	if _, err := (Stores{}).Add(NewStore{Name: "s", Location: remote}, "right"); err != nil {
		t.Fatal(err)
	}

	testutil.NewMachine(t)
	if _, err := (Stores{}).Add(NewStore{Name: "s", Location: remote}, "wrong"); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	reg, _ := store.LoadRegistry()
	dir, _ := store.DefaultDir("s")
	if len(reg.Stores) != 0 {
		t.Fatalf("registry after failed add: %+v", reg)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("clone left behind")
	}
	if _, err := (Stores{}).Add(NewStore{Name: "s", Location: remote}, "right"); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

// TestForgetLeavesNothingRegistered: when the clone can't be removed, the
// store is still forgotten and the error says what was left.
func TestForgetLeavesNothingRegistered(t *testing.T) {
	testutil.NewMachine(t)
	c, err := (Stores{}).Add(NewStore{Name: "s", Location: testutil.Remote(t)}, pass)
	if err != nil {
		t.Fatal(err)
	}
	// Make the clone unrecognizable, as if the path held something else.
	os.RemoveAll(c.Entry.Dir + "/.git")
	err = (Stores{}).Forget("s")
	if err == nil || !strings.Contains(err.Error(), "were left") {
		t.Fatalf("Forget = %v", err)
	}
	reg, _ := store.LoadRegistry()
	if len(reg.Stores) != 0 {
		t.Fatalf("store still registered: %+v", reg.Stores)
	}
}

// TestFolderStoreEndToEnd: push on one machine and pull on another through
// a shared folder, with the same fast-forward rules as git.
func TestFolderStoreEndToEnd(t *testing.T) {
	shared := t.TempDir() + "/sync/claude"
	a := testutil.NewMachine(t)
	ca, err := (Stores{}).Add(NewStore{Name: "box", Kind: store.KindFolder, Location: shared}, pass)
	if err != nil {
		t.Fatal(err)
	}
	s := localSession(t, a, "babababa-aaaa-bbbb-cccc-000000000001", `{"type":"user","uuid":"u1"}`)
	if res, err := ca.Push([]session.Session{s}, "", PushOptions{}); err != nil || len(res.Pushed) != 1 {
		t.Fatalf("push: %+v %v", res, err)
	}

	testutil.NewMachine(t)
	cb, err := (Stores{}).Add(NewStore{Name: "box", Kind: store.KindFolder, Location: shared}, pass)
	if err != nil {
		t.Fatal(err)
	}
	res, err := cb.Pull("babababa", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	appendLine(t, res.Target, `{"type":"user","uuid":"u2"}`)
	sb, _ := session.Load(res.Target)
	if r, err := cb.Push([]session.Session{sb}, "", PushOptions{}); err != nil || len(r.Pushed) != 1 {
		t.Fatalf("B push: %+v %v", r, err)
	}
	a.Use(t)
	if r, err := ca.Push([]session.Session{s}, "", PushOptions{}); err != nil || len(r.Rejected) != 1 {
		t.Fatalf("outdated push through a folder store: %+v %v", r, err)
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	for in, want := range map[string]string{
		"~":             home,
		"~/Dropbox/x":   home + "/Dropbox/x",
		"/abs/~/x":      "/abs/~/x",
		"~other/x":      "~other/x",
		"relative/path": "relative/path",
	} {
		if got := expandHome(in); filepath.ToSlash(got) != filepath.ToSlash(want) {
			t.Errorf("expandHome(%q) = %q, want %q", in, got, want)
		}
	}
}
