//go:build !unix

package platform

import "io/fs"

// On Windows and other non-unix platforms the permission and ownership checks are NOT
// enforced: mode bits do not describe ACLs and ACLs are not inspected. V2-1 promises only
// Linux (OD-08). A Windows adapter must add its own ACL check before it can promise node
// identity protection.

func checkDirAccess(dir string, info fs.FileInfo) error { return nil }

func checkFileAccess(path string, info fs.FileInfo) error { return nil }

func chmodNewDir(dir string) error { return nil }

// syncDir does nothing: directory fsync is not available here.
func syncDir(dir string) error { return nil }
