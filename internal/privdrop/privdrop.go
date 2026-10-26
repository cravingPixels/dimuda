// Package privdrop lets the container start as root, prepare its writable folders on the
// host bind mount, and then permanently become PUID:PGID, the way LinuxServer.io images do.
package privdrop

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

type Identity struct {
	UID    int
	GID    int
	Groups []int
}

// System is the set of process calls Drop needs; tests replace it with a fake.
type System struct {
	Geteuid   func() int
	Setgroups func([]int) error
	Setgid    func(int) error
	Setuid    func(int) error
	Lchown    func(string, int, int) error
}

func OS() System {
	return System{
		Geteuid:   os.Geteuid,
		Setgroups: syscall.Setgroups,
		Setgid:    syscall.Setgid,
		Setuid:    syscall.Setuid,
		Lchown:    os.Lchown,
	}
}

// Drop creates dirs, gives them to id and switches the process to id. When the process is
// not root (for example `user:` is set in compose) it only creates dirs and returns.
func Drop(sys System, id Identity, dirs ...string) (bool, error) {
	if id.UID == 0 || id.GID == 0 {
		return false, fmt.Errorf("refusing to run as root: PUID and PGID must not be 0")
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return false, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if sys.Geteuid() != 0 {
		return false, nil
	}
	for _, dir := range dirs {
		if err := chownTree(sys, dir, id); err != nil {
			return false, err
		}
	}
	// Order matters: groups and gid can only be changed while still root.
	if err := sys.Setgroups(append([]int{id.GID}, id.Groups...)); err != nil {
		return false, fmt.Errorf("setgroups: %w", err)
	}
	if err := sys.Setgid(id.GID); err != nil {
		return false, fmt.Errorf("setgid %d: %w", id.GID, err)
	}
	if err := sys.Setuid(id.UID); err != nil {
		return false, fmt.Errorf("setuid %d: %w", id.UID, err)
	}
	if sys.Geteuid() != id.UID {
		return false, fmt.Errorf("still running as uid %d after setuid %d", sys.Geteuid(), id.UID)
	}
	return true, nil
}

// chownTree also fixes files left by a previous run under a different PUID.
func chownTree(sys System, root string, id Identity) error {
	return filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := sys.Lchown(path, id.UID, id.GID); err != nil {
			return fmt.Errorf("chown %s: %w", path, err)
		}
		return nil
	})
}
