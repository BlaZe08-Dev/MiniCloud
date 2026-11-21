//go:build !unix

package platform

import "testing"

// On Windows and other non-unix platforms the permission and ownership checks are not
// enforced (ACLs are not inspected; V2-1 promises only Linux, OD-08; a Windows adapter must
// add its own ACL check), so their assertions are skipped here. Everything else (UUID format,
// content validation, concurrency, no overwrite) runs.

const permSkipReason = "unix permission and ownership checks are not enforced on this platform (ACLs are not inspected); V2-1 promises only Linux (OD-08)"

func assertCreatedModes(t *testing.T, dir, file string) {
	t.Helper()
	t.Run("permissions", func(t *testing.T) { t.Skip(permSkipReason) })
}

func makeUnwritableDir(t *testing.T, root string) (string, bool) {
	t.Helper()
	t.Skip(permSkipReason)
	return "", false
}

func TestNodeIDFile_WideMode_Refuses(t *testing.T) { t.Skip(permSkipReason) }

func TestStateDir_Existing_PermissionRules(t *testing.T) { t.Skip(permSkipReason) }

func TestStateDir_NewDirectory_ExactlyMode0700UnderUmask(t *testing.T) { t.Skip(permSkipReason) }

func TestDefaultStateDir(t *testing.T) {
	t.Setenv("USERPROFILE", `C:\Users\someone`)
	got, err := DefaultStateDir()
	if err != nil || got == "" {
		t.Fatalf("got %q, %v", got, err)
	}
}
