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
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler",
	"github.com/BlaZe08-Dev/Minicloud/internal/api",
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator",
	"github.com/BlaZe08-Dev/Minicloud/internal/worker",
	"github.com/BlaZe08-Dev/Minicloud/internal/executor",
}

func TestArchitecture_DomainPackageIndependence(t *testing.T) {
	// Locate internal/domain relative to test file or repo root
	domainDir := filepath.Join("..", "..", "internal", "domain")
	if _, err := os.Stat(domainDir); os.IsNotExist(err) {
		// Fallback to absolute or workspace relative if running from different cwd
		domainDir = filepath.Join("internal", "domain")
	}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, domainDir, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("failed to parse domain directory %s: %v", domainDir, err)
	}

	var violations []string

	for _, pkg := range pkgs {
		for fileName, file := range pkg.Files {
			// Check production domain source files (skip external test files if in separate test package)
			baseName := filepath.Base(fileName)
			if strings.HasSuffix(baseName, "_test.go") {
				continue
			}

			for _, imp := range file.Imports {
				importPath := strings.Trim(imp.Path.Value, `"`)
				for _, forbidden := range forbiddenDomainImports {
					if importPath == forbidden || strings.HasPrefix(importPath, forbidden+"/") {
						violations = append(violations, baseName+": imports forbidden package "+importPath)
					}
				}
			}
		}
	}

	if len(violations) > 0 {
		t.Errorf("Architecture boundary violation! internal/domain must not depend on transport, storage, scheduler, or execution.\nFound violations:\n%s",
			strings.Join(violations, "\n"))
	}
}
