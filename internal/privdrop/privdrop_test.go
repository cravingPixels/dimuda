package privdrop

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

type fake struct {
	euid    int
	calls   []string
	groups  []int
	chowned []string
	failAt  string
}

func (f *fake) system() System {
	step := func(name string) error {
		f.calls = append(f.calls, name)
		if f.failAt == name {
			return errors.New("denied")
		}
		return nil
	}
	return System{
		Geteuid:   func() int { return f.euid },
		Setgroups: func(g []int) error { f.groups = g; return step("setgroups") },
		Setgid:    func(int) error { return step("setgid") },
		Setuid: func(uid int) error {
			if err := step("setuid"); err != nil {
				return err
			}
			f.euid = uid
			return nil
		},
		Lchown: func(p string, _, _ int) error { f.chowned = append(f.chowned, p); return nil },
	}
}

func TestDropAsRoot(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	os.MkdirAll(filepath.Join(data, "git"), 0o755)
	os.WriteFile(filepath.Join(data, "state.json"), []byte("{}"), 0o600)

	f := &fake{euid: 0}
	dropped, err := Drop(f.system(), Identity{UID: 3001, GID: 3001, Groups: []int{3002}}, data)
	if err != nil || !dropped {
		t.Fatalf("Drop = %v, %v", dropped, err)
	}
	if !reflect.DeepEqual(f.calls, []string{"setgroups", "setgid", "setuid"}) {
		t.Errorf("calls = %v, want groups and gid before uid", f.calls)
	}
	if !reflect.DeepEqual(f.groups, []int{3001, 3002}) {
		t.Errorf("groups = %v", f.groups)
	}
	sort.Strings(f.chowned)
	want := []string{data, filepath.Join(data, "git"), filepath.Join(data, "state.json")}
	if !reflect.DeepEqual(f.chowned, want) {
		t.Errorf("chowned = %v, want %v", f.chowned, want)
	}
}

func TestDropCreatesMissingDir(t *testing.T) {
	data := filepath.Join(t.TempDir(), "app", "data")
	f := &fake{euid: 0}
	if _, err := Drop(f.system(), Identity{UID: 1, GID: 1}, data); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(data); err != nil {
		t.Errorf("data dir not created: %v", err)
	}
}

func TestDropAsNonRootOnlyCreatesDirs(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	f := &fake{euid: 3001}
	dropped, err := Drop(f.system(), Identity{UID: 3001, GID: 3001}, data)
	if err != nil || dropped || len(f.calls) != 0 || len(f.chowned) != 0 {
		t.Errorf("dropped=%v err=%v calls=%v chowned=%v", dropped, err, f.calls, f.chowned)
	}
	if _, err := os.Stat(data); err != nil {
		t.Error("dir should still be created")
	}
}

func TestDropRefusesRootIdentity(t *testing.T) {
	f := &fake{euid: 0}
	for _, id := range []Identity{{UID: 0, GID: 1}, {UID: 1, GID: 0}} {
		if _, err := Drop(f.system(), id, t.TempDir()); err == nil {
			t.Errorf("Drop(%+v) should fail", id)
		}
	}
}

func TestDropFailsFast(t *testing.T) {
	for _, step := range []string{"setgroups", "setgid", "setuid"} {
		f := &fake{euid: 0, failAt: step}
		if _, err := Drop(f.system(), Identity{UID: 3001, GID: 3001}, t.TempDir()); err == nil {
			t.Errorf("failure at %s should be returned", step)
		}
	}
}

func TestDropDetectsIneffectiveSetuid(t *testing.T) {
	f := &fake{euid: 0}
	sys := f.system()
	sys.Setuid = func(int) error { return nil }
	if _, err := Drop(sys, Identity{UID: 3001, GID: 3001}, t.TempDir()); err == nil {
		t.Error("expected error when uid did not change")
	}
}
