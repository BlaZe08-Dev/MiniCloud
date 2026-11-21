//go:build unix

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func modeOf(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// assertCreatedModes: the created directory is 0700 and the file 0600.
func assertCreatedModes(t *testing.T, dir, file string) {
	t.Helper()
	if m := modeOf(t, dir); m != 0o700 {
		t.Errorf("state directory mode = %04o, want 0700", m)
	}
	if m := modeOf(t, file); m != 0o600 {
		t.Errorf("node_id file mode = %04o, want 0600", m)
	}
}

// makeUnwritableDir returns an existing directory (0500, owned by us) that cannot be written.
func makeUnwritableDir(t *testing.T, root string) (string, bool) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not stop writes")
		return "", false
	}
	dir := filepath.Join(root, "ro")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return dir, true
}

func TestNodeIDFile_WideMode_Refuses(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660, 0o666, 0o602, 0o620, 0o601} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := writeIDFile(t, dir, otherUUID+"\n", mode)
			id, err := LoadOrCreateNodeID(dir)
			if err == nil || id != "" {
				t.Fatalf("want refusal, got %q, %v", id, err)
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("error must name the path: %v", err)
			}
			if m := modeOf(t, path); m != mode {
				t.Fatalf("refused file's mode was changed to %04o", m)
			}
			if b, _ := os.ReadFile(path); string(b) != otherUUID+"\n" {
				t.Fatalf("refused file was modified: %q", b)
			}
		})
	}
	t.Run("0400 and 0600 are accepted", func(t *testing.T) {
		for _, mode := range []os.FileMode{0o400, 0o600} {
			dir := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			writeIDFile(t, dir, otherUUID+"\n", mode)
			if got, err := LoadOrCreateNodeID(dir); err != nil || got != otherUUID {
				t.Fatalf("mode %04o: %q, %v", mode, got, err)
			}
		}
	})
}

// C2: an existing directory is inspected and never chmodded.
func TestStateDir_Existing_PermissionRules(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o750, 0o705, 0o770, 0o777, 0o701, 0o710} {
		t.Run("refuse "+mode.String(), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatal(err)
			}
			id, err := LoadOrCreateNodeID(dir)
			if err == nil || id != "" {
				t.Fatalf("want refusal, got %q, %v", id, err)
			}
			if !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), fmtMode(mode)) {
				t.Fatalf("error must name the path and the permission found (%s): %v", fmtMode(mode), err)
			}
			if m := modeOf(t, dir); m != mode {
				t.Fatalf("existing directory mode was changed from %04o to %04o", mode, m)
			}
			if got := entries(t, dir); len(got) != 0 {
				t.Fatalf("files were created in a refused directory: %v", got)
			}
		})
	}
	t.Run("accept 0700", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		id, err := LoadOrCreateNodeID(dir)
		if err != nil || id == "" {
			t.Fatalf("want ok, got %q, %v", id, err)
		}
		if m := modeOf(t, dir); m != 0o700 {
			t.Fatalf("mode = %04o", m)
		}
	})
	t.Run("accept 0500", func(t *testing.T) {
		// no group or other bit: inspected as fine; the write then fails separately.
		dir, ok := makeUnwritableDir(t, t.TempDir())
		if !ok {
			return
		}
		if err := prepareStateDir(dir); err != nil {
			t.Fatalf("0500 owned by us must pass inspection: %v", err)
		}
	})
}

func fmtMode(m os.FileMode) string {
	const d = "01234567"
	return string([]byte{'0', d[(m>>6)&7], d[(m>>3)&7], d[m&7]})
}

// A directory this call creates is exactly 0700, whatever the umask. Narrowing umasks matter:
// without the explicit chmod a 0277 umask leaves 0500.
func TestStateDir_NewDirectory_ExactlyMode0700UnderUmask(t *testing.T) {
	for _, tc := range []struct {
		name  string
		umask int
	}{
		{"permissive umask 0", 0},
		{"default umask 022", 0o022},
		{"narrowing umask 0277", 0o277},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir() // created before the umask changes
			old := syscall.Umask(tc.umask)
			defer syscall.Umask(old)

			dir := filepath.Join(parent, "state")
			id, err := LoadOrCreateNodeID(dir)
			if err != nil {
				t.Fatal(err)
			}
			if m := modeOf(t, dir); m != 0o700 {
				t.Fatalf("directory mode = %04o, want exactly 0700", m)
			}
			if m := modeOf(t, filepath.Join(dir, NodeIDFileName)); m != 0o600 {
				t.Fatalf("file mode = %04o, want exactly 0600", m)
			}
			if again, err := LoadOrCreateNodeID(dir); err != nil || again != id {
				t.Fatalf("reuse under umask: %q, %v", again, err)
			}
		})
	}
}

func TestStateDir_MissingParentsCreated0700OnlyFinalChmodded(t *testing.T) {
	root := t.TempDir()
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	dir := filepath.Join(root, "a", "b", "state")
	if _, err := LoadOrCreateNodeID(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(root, "a"), filepath.Join(root, "a", "b"), dir} {
		if m := modeOf(t, p); m != 0o700 {
			t.Errorf("%s mode = %04o, want 0700", p, m)
		}
	}
}

// The ownership refusal needs a directory owned by someone else, which cannot be made without
// root, so the real path (os.Geteuid) is only exercised for the accepting case; the refusal
// logic is tested by passing a different uid to checkOwner.
func TestStateDir_OwnerCheck(t *testing.T) {
	dir := t.TempDir()
	fi, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkOwner("node state directory", dir, fi, os.Geteuid()); err != nil {
		t.Fatalf("own directory must pass: %v", err)
	}
	err = checkOwner("node state directory", dir, fi, os.Geteuid()+1)
	if err == nil || !strings.Contains(err.Error(), "not by the current user") || !strings.Contains(err.Error(), dir) {
		t.Fatalf("a directory of another uid must be refused naming the path, got %v", err)
	}
}

// AUD-03: an ID file owned by another user is refused. Same technique as the directory test: a
// different uid is passed, so no root is needed.
func TestNodeIDFile_OwnerCheck(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := writeIDFile(t, dir, otherUUID+"\n", 0o600)
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkFileAccessAs(path, fi, os.Geteuid()); err != nil {
		t.Fatalf("own file must pass: %v", err)
	}
	err = checkFileAccessAs(path, fi, os.Geteuid()+1)
	if err == nil || !strings.Contains(err.Error(), "not by the current user") || !strings.Contains(err.Error(), path) {
		t.Fatalf("a file of another uid must be refused naming the path, got %v", err)
	}
	// The production path (os.Geteuid) still accepts our own file end to end.
	if got, err := LoadOrCreateNodeID(dir); err != nil || got != otherUUID {
		t.Fatalf("own 0600 file: %q, %v", got, err)
	}
}

func TestDefaultStateDir(t *testing.T) {
	t.Run("home is used", func(t *testing.T) {
		t.Setenv("HOME", "/home/someone")
		got, err := DefaultStateDir()
		if err != nil || got != "/home/someone/.minicloud" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("unresolvable home is an error, never a fallback", func(t *testing.T) {
		t.Setenv("HOME", "")
		got, err := DefaultStateDir()
		if err == nil || got != "" {
			t.Fatalf("want an error and no directory, got %q, %v", got, err)
		}
		if !strings.Contains(err.Error(), "MINICLOUD_NODE_STATE_DIR") {
			t.Fatalf("error should point at the setting: %v", err)
		}
	})
}
