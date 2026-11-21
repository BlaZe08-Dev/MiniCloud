package architecture_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forbiddenDomainImports defines packages that internal/domain is strictly prohibited from importing.
var forbiddenDomainImports = []string{
	"net/http",
	"github.com/BlaZe08-Dev/Minicloud/internal/storage",
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres",
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler",
	"github.com/BlaZe08-Dev/Minicloud/internal/api",
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator",
	"github.com/BlaZe08-Dev/Minicloud/internal/worker",
	"github.com/BlaZe08-Dev/Minicloud/internal/executor",
	"github.com/BlaZe08-Dev/Minicloud/internal/queue",
	"github.com/jackc/pgx",
}

// forbiddenCoordinatorImports defines packages that internal/coordinator must not import.
// (Coordinator depends on storage.Repository interface, not pgx or postgres implementation directly).
var forbiddenCoordinatorImports = []string{
	"github.com/jackc/pgx",
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres",
}

// forbiddenExecutorImports defines packages that internal/executor must not import.
var forbiddenExecutorImports = []string{
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler",
	"github.com/BlaZe08-Dev/Minicloud/internal/queue",
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator",
	"github.com/BlaZe08-Dev/Minicloud/internal/api",
}

// forbiddenQueueImports defines packages that internal/queue must not import.
var forbiddenQueueImports = []string{
	"github.com/BlaZe08-Dev/Minicloud/internal/api",
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator",
	"github.com/BlaZe08-Dev/Minicloud/internal/worker",
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler",
	"github.com/jackc/pgx",
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres",
}

func checkPackageBoundaries(t *testing.T, relDir string, forbidden []string, boundaryDescription string) {
	pkgDir := filepath.Join("..", "..", relDir)
	if _, err := os.Stat(pkgDir); os.IsNotExist(err) {
		pkgDir = relDir
	}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, pkgDir, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("failed to parse directory %s: %v", pkgDir, err)
	}

	var violations []string

	for _, pkg := range pkgs {
		for fileName, file := range pkg.Files {
			baseName := filepath.Base(fileName)
			if strings.HasSuffix(baseName, "_test.go") {
				continue
			}

			for _, imp := range file.Imports {
				importPath := strings.Trim(imp.Path.Value, `"`)
				for _, f := range forbidden {
					if importPath == f || strings.HasPrefix(importPath, f+"/") {
						violations = append(violations, baseName+": imports forbidden package "+importPath)
					}
				}
			}
		}
	}

	if len(violations) > 0 {
		t.Errorf("Architecture boundary violation! %s\nFound violations:\n%s",
			boundaryDescription, strings.Join(violations, "\n"))
	}
}

func TestArchitecture_DomainPackageIndependence(t *testing.T) {
	checkPackageBoundaries(t, filepath.Join("internal", "domain"), forbiddenDomainImports,
		"internal/domain must not depend on transport, storage, scheduler, queue, execution, or pgx.")
}

func TestArchitecture_CoordinatorPackageIndependence(t *testing.T) {
	checkPackageBoundaries(t, filepath.Join("internal", "coordinator"), forbiddenCoordinatorImports,
		"internal/coordinator must not depend directly on pgx or internal/storage/postgres.")
}

func TestArchitecture_ExecutorIndependence(t *testing.T) {
	checkPackageBoundaries(t, filepath.Join("internal", "executor"), forbiddenExecutorImports,
		"internal/executor must not depend on scheduler, queue, coordinator, or api.")
}

func TestArchitecture_QueueIndependence(t *testing.T) {
	checkPackageBoundaries(t, filepath.Join("internal", "queue"), forbiddenQueueImports,
		"internal/queue must not depend on api, coordinator, worker, or scheduler.")
}

// modulePath is the import path prefix of every project package.
const modulePath = "github.com/BlaZe08-Dev/Minicloud/"

// platformPackage is the OS adapter of ADR-0022 Section 6.2.
const platformPackage = modulePath + "internal/platform"

// forbiddenPlatformImports is what the core packages must not import: the OS adapter is used
// by the worker (later slices) only.
var forbiddenPlatformImports = []string{platformPackage}

// packageDirs returns relDir and every directory below it, so a rule covers subpackages
// (internal/storage/postgres, internal/storage/storagetest).
func packageDirs(t *testing.T, relDir string) []string {
	root := filepath.Join("..", "..", relDir)
	if _, err := os.Stat(root); os.IsNotExist(err) {
		root = relDir
	}
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() != "testdata" {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to walk %s: %v", relDir, err)
	}
	return dirs
}

// TestArchitecture_PlatformIsolation enforces ADR-0022 Section 6.2: internal/domain,
// internal/coordinator, internal/storage (and its subpackages) and internal/api never import
// internal/platform, and internal/platform imports internal/domain only among project packages.
func TestArchitecture_PlatformIsolation(t *testing.T) {
	for _, relDir := range []string{
		filepath.Join("internal", "domain"),
		filepath.Join("internal", "coordinator"),
		filepath.Join("internal", "storage"),
		filepath.Join("internal", "api"),
	} {
		for _, dir := range packageDirs(t, relDir) {
			checkPackageBoundaries(t, dir, forbiddenPlatformImports,
				"internal/domain, internal/coordinator, internal/storage and internal/api must not import internal/platform (ADR-0022 Section 6.2).")
		}
	}

	// internal/platform may import internal/domain and nothing else of the project.
	pkgDir := filepath.Join("..", "..", "internal", "platform")
	if _, err := os.Stat(pkgDir); os.IsNotExist(err) {
		pkgDir = filepath.Join("internal", "platform")
	}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, pkgDir, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("failed to parse directory %s: %v", pkgDir, err)
	}
	var violations []string
	sawDomain := false
	for _, pkg := range pkgs {
		for fileName, file := range pkg.Files {
			baseName := filepath.Base(fileName)
			if strings.HasSuffix(baseName, "_test.go") {
				continue
			}
			for _, imp := range file.Imports {
				importPath := strings.Trim(imp.Path.Value, `"`)
				if !strings.HasPrefix(importPath, modulePath) {
					continue
				}
				if importPath == modulePath+"internal/domain" {
					sawDomain = true
					continue
				}
				violations = append(violations, baseName+": imports project package "+importPath)
			}
		}
	}
	if len(violations) > 0 {
		t.Errorf("Architecture boundary violation! internal/platform may import only internal/domain among project packages.\nFound violations:\n%s",
			strings.Join(violations, "\n"))
	}
	if !sawDomain {
		t.Errorf("internal/platform no longer imports internal/domain; the rule or the package moved, check this test")
	}
}
