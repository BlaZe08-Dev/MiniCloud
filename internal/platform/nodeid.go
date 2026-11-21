package platform

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// NodeIDFileName is the name of the node ID file inside the state directory (ADR-0022 Section 5).
const NodeIDFileName = "node_id"

// tempPrefix names the temporary files created inside the state directory during first
// creation. They are always removed again.
const tempPrefix = ".node_id.tmp-"

// maxNodeIDFileBytes bounds what is read from the ID file: a UUID plus one newline. A larger
// file is invalid content and is refused without reading it all.
const maxNodeIDFileBytes = domain.NodeIDLength + 1

// DefaultStateDir returns the default node state directory, ~/.minicloud (ADR-0022 Section
// 4.5). It returns an error when the home directory cannot be resolved; the caller must then
// stop and never pick another place.
//
// Like LoadOrCreateNodeID it must be called only when node reporting is enabled
// (MINICLOUD_NODE_REPORTING_ENABLED is true); V2-1d decides that. With reporting off nothing
// here may run.
func DefaultStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot resolve the home directory for the default node state directory ~/.minicloud (set MINICLOUD_NODE_STATE_DIR): %w", err)
	}
	if home == "" {
		return "", errors.New("cannot resolve the home directory for the default node state directory ~/.minicloud (set MINICLOUD_NODE_STATE_DIR): home directory is empty")
	}
	return filepath.Join(home, ".minicloud"), nil
}

// idOps holds the operations that tests replace to inject failures. Production uses
// defaultIDOps.
type idOps struct {
	rand    func(b []byte) error
	write   func(f *os.File, b []byte) error
	sync    func(f *os.File) error
	link    func(oldname, newname string) error
	syncDir func(dir string) error
}

func defaultIDOps() idOps {
	return idOps{
		rand:    func(b []byte) error { _, err := io.ReadFull(rand.Reader, b); return err },
		write:   func(f *os.File, b []byte) error { _, err := f.Write(b); return err },
		sync:    func(f *os.File) error { return f.Sync() },
		link:    os.Link,
		syncDir: syncDir,
	}
}

// LoadOrCreateNodeID returns the node ID kept in dir/node_id, creating it on first use
// (ADR-0022 Section 5). The ID is 16 bytes from crypto/rand formatted as a lowercase RFC 9562
// version 4 UUID. The directory is 0700 and the file 0600.
//
// It must be called only when node reporting is enabled (MINICLOUD_NODE_REPORTING_ENABLED is
// true): the state directory is read or created only with the owner's consent. V2-1d decides
// that; with reporting off this function must not run.
//
// Directory rules. If dir does not exist this call creates it (missing parents 0700) and
// chmods only the final directory to exactly 0700, so the umask cannot widen or narrow it. If
// dir already exists it is inspected and never modified: it is refused when it is a symlink or
// not a directory, when any group or other permission bit is set, or (unix) when it is not
// owned by the current user.
//
// File rules. An existing node_id is refused, never overwritten and never replaced by a new
// ID, when it is a symlink or not a regular file, when its mode has any group or other bit,
// or when its content is not a valid UUID. First creation is race safe: the ID is written to
// a temporary file inside dir (0600), fsynced, then published with os.Link, which fails with
// EEXIST if node_id exists, so it can never replace another process's ID. os.Rename is never
// used and the final name is never written directly. If publication reports EEXIST the
// existing file is read and validated by the same rules. The temporary file is removed on
// every path. If hard links are unsupported the call fails with a clear error; it does not
// fall back. After publication the directory is fsynced where the platform supports it.
//
// Every error names the path. The function never creates an identity in another directory.
//
// Windows and other non-unix platforms: the permission and ownership checks are not enforced
// (ACLs are not inspected). V2-1 promises only Linux (OD-08); a Windows adapter must add its
// own ACL check.
func LoadOrCreateNodeID(dir string) (string, error) {
	return loadOrCreateNodeID(dir, defaultIDOps())
}

func loadOrCreateNodeID(dir string, ops idOps) (string, error) {
	if dir == "" {
		return "", errors.New("node state directory is empty")
	}
	dir = filepath.Clean(dir)

	if err := prepareStateDir(dir); err != nil {
		return "", err
	}

	final := filepath.Join(dir, NodeIDFileName)
	id, err := readExistingNodeID(final)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return createNodeID(dir, final, ops)
}

// prepareStateDir creates the directory when it is missing and otherwise inspects it. It never
// changes the permissions of an existing directory.
func prepareStateDir(dir string) error {
	info, err := os.Lstat(dir)
	switch {
	case err == nil:
		return inspectExistingDir(dir, info)
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("node state directory %s: cannot inspect: %w", dir, err)
	}

	// Missing: create only what is needed, 0700. Mkdir on the final component tells us
	// whether this call created it; EEXIST means a racing process did, and then it is an
	// existing directory to inspect.
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return fmt.Errorf("node state directory %s: cannot create parent directory: %w", dir, err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("node state directory %s: cannot create: %w", dir, err)
		}
		info, lerr := os.Lstat(dir)
		if lerr != nil {
			return fmt.Errorf("node state directory %s: cannot inspect: %w", dir, lerr)
		}
		return inspectExistingDir(dir, info)
	}
	if err := chmodNewDir(dir); err != nil {
		return fmt.Errorf("node state directory %s: cannot set mode 0700: %w", dir, err)
	}
	info, err = os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("node state directory %s: cannot inspect after creation: %w", dir, err)
	}
	return inspectExistingDir(dir, info)
}

// inspectExistingDir applies the C2 refusal rules. Platform specific permission and ownership
// checks are in nodeid_unix.go and nodeid_other.go.
func inspectExistingDir(dir string, info fs.FileInfo) error {
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("node state directory %s is a symlink; refusing to use it", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("node state directory %s exists but is not a directory", dir)
	}
	return checkDirAccess(dir, info)
}

// readExistingNodeID returns the valid ID in final, an error wrapping fs.ErrNotExist when the
// file is absent, and any other error when the file must be refused.
func readExistingNodeID(final string) (string, error) {
	info, err := os.Lstat(final)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		return "", fmt.Errorf("node id file %s: cannot inspect: %w", final, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("node id file %s is a symlink; refusing to use it", final)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("node id file %s is not a regular file; refusing to use it", final)
	}
	if err := checkFileAccess(final, info); err != nil {
		return "", err
	}
	if info.Size() > maxNodeIDFileBytes {
		return "", fmt.Errorf("node id file %s is invalid: %d bytes, expected a UUID; not overwriting it", final, info.Size())
	}
	f, err := os.Open(final)
	if err != nil {
		return "", fmt.Errorf("node id file %s: cannot read: %w", final, err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxNodeIDFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("node id file %s: cannot read: %w", final, err)
	}
	id := strings.TrimSuffix(string(b), "\n")
	if verr := domain.ValidateNodeID(id); verr != nil {
		return "", fmt.Errorf("node id file %s is invalid: %v; not overwriting it", final, verr)
	}
	return id, nil
}

// createNodeID writes a temporary file in dir and publishes it with os.Link.
func createNodeID(dir, final string, ops idOps) (id string, err error) {
	var raw [16]byte
	if err := ops.rand(raw[:]); err != nil {
		return "", fmt.Errorf("node id file %s: cannot read random bytes: %w", final, err)
	}
	id = domain.FormatNodeID(raw)

	var suffix [8]byte
	if err := ops.rand(suffix[:]); err != nil {
		return "", fmt.Errorf("node id file %s: cannot read random bytes: %w", final, err)
	}
	tmp := filepath.Join(dir, tempPrefix+hex.EncodeToString(suffix[:]))

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("node id file %s: cannot create temporary file %s: %w", final, tmp, err)
	}
	// The temporary file is removed on every path, success included (the final name is a
	// separate hard link to the same data).
	defer func() {
		if rerr := os.Remove(tmp); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) && err == nil {
			id, err = "", fmt.Errorf("node id file %s: cannot remove temporary file %s: %w", final, tmp, rerr)
		}
	}()

	if err := ops.write(f, []byte(id+"\n")); err != nil {
		f.Close()
		return "", fmt.Errorf("node id file %s: cannot write temporary file %s: %w", final, tmp, err)
	}
	if err := ops.sync(f); err != nil {
		f.Close()
		return "", fmt.Errorf("node id file %s: cannot sync temporary file %s: %w", final, tmp, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("node id file %s: cannot close temporary file %s: %w", final, tmp, err)
	}
	// Exactly 0600 regardless of the umask.
	if err := os.Chmod(tmp, 0o600); err != nil {
		return "", fmt.Errorf("node id file %s: cannot set mode 0600 on %s: %w", final, tmp, err)
	}

	if err := ops.link(tmp, final); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Another process published first: read and validate its file, never replace it.
			existing, rerr := readExistingNodeID(final)
			if rerr != nil {
				return "", rerr
			}
			return existing, nil
		}
		return "", fmt.Errorf("node id file %s: cannot publish: %w (hard links are required on this filesystem; there is no rename or direct-write fallback)", final, err)
	}

	if err := ops.syncDir(dir); err != nil {
		return "", fmt.Errorf("node state directory %s: cannot sync after publishing %s: %w", dir, final, err)
	}
	return id, nil
}
