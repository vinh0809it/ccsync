package app

import (
	"os"
	"strings"
	"testing"

	"github.com/vinh-vh/ccsync/internal/session"
	"github.com/vinh-vh/ccsync/internal/store"
	"github.com/vinh-vh/ccsync/internal/testutil"
)

// TestSeveralStores: one machine, two stores with their own passphrases.
func TestSeveralStores(t *testing.T) {
	m := testutil.NewMachine(t)
	work, home := testutil.Remote(t), testutil.Remote(t)
	stores := Stores{}
	if _, err := stores.Add("work", work, "work-pass"); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Add("home", home, "home-pass"); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Add("work", home, "x"); err == nil {
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
	c, err := stores.Add("main", old, "lost-pass")
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
	c, err = stores.Add("main", fresh, "new-pass")
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
	if _, err := (Stores{}).Add("s", remote, "right"); err != nil {
		t.Fatal(err)
	}

	testutil.NewMachine(t)
	if _, err := (Stores{}).Add("s", remote, "wrong"); err == nil {
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
	if _, err := (Stores{}).Add("s", remote, "right"); err != nil {
		t.Fatalf("retry: %v", err)
	}
}
