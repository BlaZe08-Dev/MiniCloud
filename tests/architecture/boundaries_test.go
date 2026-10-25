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
