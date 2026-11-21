//go:build unix

package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// checkDirAccess refuses an existing state directory with any group or other permission bit
// or one that is not owned by the current user. It never changes the directory.
func checkDirAccess(dir string, info fs.FileInfo) error {
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("node state directory %s has permissions %04o; group and other access must be off (0700); refusing, not changing it", dir, perm)
	}
	return checkOwner(dir, info, os.Geteuid())
}

// checkOwner refuses a path whose owner uid differs from euid. The uid is a parameter so the
// refusal can be tested without root.
func checkOwner(path string, info fs.FileInfo, euid int) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("node state directory %s: cannot determine owner", path)
	}
	if int(st.Uid) != euid {
		return fmt.Errorf("node state directory %s is owned by uid %d, not by the current user (uid %d); refusing", path, st.Uid, euid)
	}
	return nil
}

// checkFileAccess refuses an ID file with any group or other permission bit.
func checkFileAccess(path string, info fs.FileInfo) error {
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("node id file %s has permissions %04o; group and other access must be off (0600); refusing, not changing it", path, perm)
	}
	return nil
}

// chmodNewDir sets a directory this call created to exactly 0700.
func chmodNewDir(dir string) error { return os.Chmod(dir, 0o700) }

// syncDir fsyncs a directory so the new directory entry is durable. EINVAL (a filesystem that
// cannot fsync a directory) is not an error.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}
