package platform

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

const otherUUID = "11111111-2222-4333-8444-555555555555"

func entries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func onlyNodeIDFile(t *testing.T, dir string) {
	t.Helper()
	if got := entries(t, dir); len(got) != 1 || got[0] != NodeIDFileName {
		t.Fatalf("directory %s entries = %v, want exactly [%s] (no temp files)", dir, got, NodeIDFileName)
	}
}

// writeIDFile writes a node_id file with an exact mode, independent of the umask.
func writeIDFile(t *testing.T, dir, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, NodeIDFileName)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func newStateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	return dir
}

func TestNodeIDFile_CreateThenReuse(t *testing.T) {
	dir := newStateDir(t)
	first, err := LoadOrCreateNodeID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := domain.ValidateNodeID(first); err != nil {
		t.Fatalf("created id is not a lowercase v4 UUID: %v", err)
	}
	assertCreatedModes(t, dir, filepath.Join(dir, NodeIDFileName))
	onlyNodeIDFile(t, dir)

	raw, err := os.ReadFile(filepath.Join(dir, NodeIDFileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != first+"\n" {
		t.Fatalf("file content = %q, want the id and one newline", raw)
	}

	second, err := LoadOrCreateNodeID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("second start returned %s, want the same %s", second, first)
	}
	onlyNodeIDFile(t, dir)
}

func TestNodeIDFile_AcceptsFileWithOrWithoutNewline(t *testing.T) {
	for _, content := range []string{otherUUID, otherUUID + "\n"} {
		dir := newStateDir(t)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		writeIDFile(t, dir, content, 0o600)
		got, err := LoadOrCreateNodeID(dir)
		if err != nil || got != otherUUID {
			t.Fatalf("content %q: got %q, %v", content, got, err)
		}
	}
}

func TestNodeIDFile_ConcurrentFirstStart_SameID(t *testing.T) {
	const n = 16
	dir := newStateDir(t)
	ids := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ids[i], errs[i] = LoadOrCreateNodeID(dir)
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("goroutine %d got %s, goroutine 0 got %s: not one UUID", i, ids[i], ids[0])
		}
	}
	onlyNodeIDFile(t, dir)
	raw, _ := os.ReadFile(filepath.Join(dir, NodeIDFileName))
	if strings.TrimSpace(string(raw)) != ids[0] {
		t.Fatalf("file holds %q, goroutines returned %s", raw, ids[0])
	}
}

// TestNodeIDFile_HelperProcesses_RaceOneDirectory re-executes the test binary: several real
// processes race on one empty (not yet created) directory and must all return the same UUID,
// leave exactly one node_id file and no temp files.
func TestNodeIDFile_HelperProcesses_RaceOneDirectory(t *testing.T) {
	const procs = 6
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	gate := filepath.Join(root, "gate")

	cmds := make([]*exec.Cmd, procs)
	outs := make([]*bytes.Buffer, procs)
	errs := make([]*bytes.Buffer, procs)
	for i := range cmds {
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), helperDirEnv+"="+dir, helperGateEnv+"="+gate)
		outs[i], errs[i] = &bytes.Buffer{}, &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = outs[i], errs[i]
		if err := cmd.Start(); err != nil {
			t.Fatalf("start helper %d: %v", i, err)
		}
		cmds[i] = cmd
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper %d failed: %v; stderr: %s", i, err, errs[i])
		}
		ids = append(ids, strings.TrimSpace(outs[i].String()))
	}
	for i, id := range ids {
		if err := domain.ValidateNodeID(id); err != nil {
			t.Fatalf("helper %d printed %q: %v", i, id, err)
		}
		if id != ids[0] {
			t.Fatalf("helper %d returned %s, helper 0 returned %s", i, id, ids[0])
		}
	}
	onlyNodeIDFile(t, dir)
}

func TestNodeIDFile_CorruptFile_Refuses(t *testing.T) {
	cases := map[string]string{
		"empty":           "",
		"garbage":         "not a uuid\n",
		"uppercase":       "3F1C9A2E-6B7D-4E21-9C4A-0D5E8F7A1B23\n",
		"version 1":       "3f1c9a2e-6b7d-1e21-9c4a-0d5e8f7a1b23\n",
		"two lines":       otherUUID + "\n" + otherUUID + "\n",
		"leading space":   " " + otherUUID + "\n",
		"trailing spaces": otherUUID + "  \n",
		"crlf":            otherUUID + "\r\n",
		"truncated":       otherUUID[:20],
		"binary":          "\x00\x01\x02",
		"huge":            strings.Repeat("a", 1<<20),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := newStateDir(t)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := writeIDFile(t, dir, content, 0o600)

			id, err := LoadOrCreateNodeID(dir)
			if err == nil {
				t.Fatalf("want refusal, got id %q", id)
			}
			if id != "" {
				t.Fatalf("a refusal must not return an id, got %q", id)
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("error must name the path %s: %v", path, err)
			}
			got, rerr := os.ReadFile(path)
			if rerr != nil || string(got) != content {
				t.Fatalf("corrupt file was modified or removed: %q, %v", got, rerr)
			}
			onlyNodeIDFile(t, dir)
		})
	}
}

func TestNodeIDFile_Symlink_Refuses(t *testing.T) {
	t.Run("symlink to a valid file", func(t *testing.T) {
		dir := newStateDir(t)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.WriteFile(target, []byte(otherUUID+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, NodeIDFileName)
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks not available here: %v", err)
		}
		id, err := LoadOrCreateNodeID(dir)
		if err == nil || !strings.Contains(err.Error(), "symlink") || !strings.Contains(err.Error(), link) {
			t.Fatalf("want a symlink refusal naming %s, got id %q err %v", link, id, err)
		}
		if fi, lerr := os.Lstat(link); lerr != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("symlink was replaced: %v %v", fi, lerr)
		}
		if b, _ := os.ReadFile(target); string(b) != otherUUID+"\n" {
			t.Fatalf("symlink target was modified: %q", b)
		}
	})
	t.Run("dangling symlink is not written through", func(t *testing.T) {
		dir := newStateDir(t)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "does-not-exist")
		if err := os.Symlink(target, filepath.Join(dir, NodeIDFileName)); err != nil {
			t.Skipf("symlinks not available here: %v", err)
		}
		if _, err := LoadOrCreateNodeID(dir); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("want a symlink refusal, got %v", err)
		}
		if _, serr := os.Lstat(target); serr == nil {
			t.Fatal("a file was created through the dangling symlink")
		}
	})
	t.Run("not a regular file", func(t *testing.T) {
		dir := newStateDir(t)
		if err := os.MkdirAll(filepath.Join(dir, NodeIDFileName), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreateNodeID(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("want a not-a-regular-file refusal, got %v", err)
		}
	})
}

func TestStateDir_Symlink_Refuses(t *testing.T) {
	real := filepath.Join(t.TempDir(), "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks not available here: %v", err)
	}
	_, err := LoadOrCreateNodeID(link)
	if err == nil || !strings.Contains(err.Error(), "symlink") || !strings.Contains(err.Error(), link) {
		t.Fatalf("want a symlink refusal naming %s, got %v", link, err)
	}
	if got := entries(t, real); len(got) != 0 {
		t.Fatalf("a file was created through the symlinked directory: %v", got)
	}
}

func TestStateDir_NotADirectory_Refuses(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadOrCreateNodeID(f)
	if err == nil || !strings.Contains(err.Error(), "not a directory") || !strings.Contains(err.Error(), f) {
		t.Fatalf("want a not-a-directory refusal naming %s, got %v", f, err)
	}
}

func TestStateDir_Empty_Refuses(t *testing.T) {
	if _, err := LoadOrCreateNodeID(""); err == nil {
		t.Fatal("an empty directory must be refused, never defaulted to the current directory")
	}
}

func hasNodeIDFile(t *testing.T, root string) bool {
	t.Helper()
	found := false
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (d.Name() == NodeIDFileName || strings.HasPrefix(d.Name(), tempPrefix)) {
			found = true
		}
		return nil
	})
	return found
}

// TestNodeIDFile_DirectoryUnwritable_Refuses: a state directory that cannot be created or is
// not writable, and a failing write of the ID file, make start fail with an error; no node_id
// file (and no temp file) exists anywhere afterwards. That no registration is sent belongs to
// the worker (V2-1d); this package sends nothing.
func TestNodeIDFile_DirectoryUnwritable_Refuses(t *testing.T) {
	t.Run("parent is a regular file so the directory cannot be created", func(t *testing.T) {
		root := t.TempDir()
		blocker := filepath.Join(root, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(blocker, "state")
		id, err := LoadOrCreateNodeID(dir)
		if err == nil || id != "" {
			t.Fatalf("want an error and no id, got %q, %v", id, err)
		}
		if !strings.Contains(err.Error(), dir) {
			t.Fatalf("error must name the path %s: %v", dir, err)
		}
		if hasNodeIDFile(t, root) {
			t.Fatal("a node_id file exists after a refusal")
		}
	})
	t.Run("existing directory is not writable", func(t *testing.T) {
		root := t.TempDir()
		dir, ok := makeUnwritableDir(t, root)
		if !ok {
			return
		}
		id, err := LoadOrCreateNodeID(dir)
		if err == nil || id != "" {
			t.Fatalf("want an error and no id, got %q, %v", id, err)
		}
		if !strings.Contains(err.Error(), dir) {
			t.Fatalf("error must name the path %s: %v", dir, err)
		}
		if hasNodeIDFile(t, root) {
			t.Fatal("a node_id file exists after a refusal")
		}
	})
	t.Run("write of the ID file fails", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "state")
		ops := defaultIDOps()
		ops.write = func(f *os.File, b []byte) error { return errors.New("injected disk full") }
		id, err := loadOrCreateNodeID(dir, ops)
		if err == nil || id != "" || !strings.Contains(err.Error(), "injected disk full") {
			t.Fatalf("want the write error, got %q, %v", id, err)
		}
		if hasNodeIDFile(t, root) {
			t.Fatal("a node_id or temp file exists after a failed write")
		}
	})
	t.Run("fsync of the temp file fails", func(t *testing.T) {
		root := t.TempDir()
		ops := defaultIDOps()
		ops.sync = func(f *os.File) error { return errors.New("injected fsync failure") }
		if _, err := loadOrCreateNodeID(filepath.Join(root, "state"), ops); err == nil {
			t.Fatal("want an error")
		}
		if hasNodeIDFile(t, root) {
			t.Fatal("a node_id or temp file exists after a failed fsync")
		}
	})
	t.Run("random source fails", func(t *testing.T) {
		root := t.TempDir()
		ops := defaultIDOps()
		ops.rand = func(b []byte) error { return errors.New("injected entropy failure") }
		if _, err := loadOrCreateNodeID(filepath.Join(root, "state"), ops); err == nil {
			t.Fatal("want an error")
		}
		if hasNodeIDFile(t, root) {
			t.Fatal("a node_id file exists after a failed random read")
		}
	})
}

// TestNodeIDFile_FailedPublish_RemovesTempFile: after a failed publish no temp file remains.
func TestNodeIDFile_FailedPublish_RemovesTempFile(t *testing.T) {
	dir := newStateDir(t)
	ops := defaultIDOps()
	ops.link = func(oldname, newname string) error { return errors.New("injected link failure") }
	id, err := loadOrCreateNodeID(dir, ops)
	if err == nil || id != "" {
		t.Fatalf("want an error and no id, got %q, %v", id, err)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Fatalf("directory entries after a failed publish = %v, want none (temp file must be removed)", got)
	}
}

// TestNodeIDFile_LinkUnsupported_NoFallback: if hard links are unsupported the call fails with
// a clear error; it does not fall back to rename or to a direct write.
func TestNodeIDFile_LinkUnsupported_NoFallback(t *testing.T) {
	dir := newStateDir(t)
	ops := defaultIDOps()
	ops.link = func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: errors.ErrUnsupported}
	}
	_, err := loadOrCreateNodeID(dir, ops)
	if err == nil || !strings.Contains(err.Error(), "hard links are required") {
		t.Fatalf("want a clear hard-link error, got %v", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, NodeIDFileName)) {
		t.Fatalf("error must name the path: %v", err)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Fatalf("no file may be created without hard links, got %v", got)
	}
}

// TestNodeIDFile_ExistingValidID_NeverReplacedWhenPublicationRaces: another process publishes a
// valid node_id between our existence check and our publish. The existing file must survive and
// its ID must be returned. This is what separates os.Link from os.Rename.
func TestNodeIDFile_ExistingValidID_NeverReplacedWhenPublicationRaces(t *testing.T) {
	dir := newStateDir(t)
	final := filepath.Join(dir, NodeIDFileName)
	realLink := defaultIDOps().link
	ops := defaultIDOps()
	ops.link = func(oldname, newname string) error {
		// the competitor wins the race just before our publish
		if err := os.WriteFile(newname, []byte(otherUUID+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return realLink(oldname, newname)
	}
	got, err := loadOrCreateNodeID(dir, ops)
	if err != nil {
		t.Fatal(err)
	}
	if got != otherUUID {
		t.Fatalf("returned %s, want the existing id %s", got, otherUUID)
	}
	b, err := os.ReadFile(final)
	if err != nil || string(b) != otherUUID+"\n" {
		t.Fatalf("existing node_id was replaced: %q, %v", b, err)
	}
	onlyNodeIDFile(t, dir)
}

// A racing corrupt file is refused, not replaced and not regenerated.
func TestNodeIDFile_RacingCorruptFile_Refuses(t *testing.T) {
	dir := newStateDir(t)
	final := filepath.Join(dir, NodeIDFileName)
	realLink := defaultIDOps().link
	ops := defaultIDOps()
	ops.link = func(oldname, newname string) error {
		if err := os.WriteFile(newname, []byte("garbage\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return realLink(oldname, newname)
	}
	id, err := loadOrCreateNodeID(dir, ops)
	if err == nil || id != "" {
		t.Fatalf("want a refusal, got %q, %v", id, err)
	}
	if b, _ := os.ReadFile(final); string(b) != "garbage\n" {
		t.Fatalf("racing corrupt file was overwritten: %q", b)
	}
	onlyNodeIDFile(t, dir)
}

func TestNodeIDFile_DirSyncFailureIsReported(t *testing.T) {
	dir := newStateDir(t)
	ops := defaultIDOps()
	ops.syncDir = func(string) error { return errors.New("injected dir sync failure") }
	_, err := loadOrCreateNodeID(dir, ops)
	if err == nil || !strings.Contains(err.Error(), "injected dir sync failure") {
		t.Fatalf("want the directory sync error, got %v", err)
	}
}

func TestNodeIDFile_UniqueIDsPerDirectory(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		id, err := LoadOrCreateNodeID(filepath.Join(t.TempDir(), fmt.Sprintf("s%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
}
