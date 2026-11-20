// Package version holds the MiniCloud release version the worker reports at registration.
package version

// Version is a var, not a const, so a release build can override it:
//
//	go build -ldflags "-X github.com/BlaZe08-Dev/Minicloud/internal/version.Version=<tag>" ./...
//
// The value must equal the release tag.
var Version = "v1.1.0"
