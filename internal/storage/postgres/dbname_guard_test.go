package postgres_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// isTestDatabaseName is the name check behind every "SAFETY VIOLATION" guard: tests
// that create and drop schemas refuse any database whose name does not end in "_test".
func isTestDatabaseName(name string) bool {
	return strings.HasSuffix(name, "_test")
}

func TestIsTestDatabaseName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"minicloud_claude_test", true},
		{"minicloud_test", true},
		{"_test", true},
		{"minicloud", false},
		{"postgres", false},
		{"minicloud_test_backup", false},
		{"minicloud_testx", false},
		{"test", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isTestDatabaseName(tc.name); got != tc.want {
			t.Errorf("isTestDatabaseName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// SEC-023: every test function that reads MINICLOUD_TEST_DATABASE_URL must either go
// through a setup helper (which carries the guard) or apply the name check itself.
// Static check (go/ast): it never connects to a database.
func TestEveryTestURLConsumerHasNameGuard(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob test files: %v (%d files)", err, len(files))
	}
	fset := token.NewFileSet()
	consumers := 0
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !readsTestURL(fn.Body) {
				continue
			}
			consumers++
			if !hasNameGuard(fn.Body) {
				t.Errorf("%s in %s reads MINICLOUD_TEST_DATABASE_URL without a database-name guard (SEC-023)", fn.Name.Name, f)
			}
		}
	}
	if consumers == 0 {
		t.Fatal("found no URL consumer; the scan itself is broken")
	}
}

func readsTestURL(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok && len(c.Args) == 1 {
			if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Getenv" {
				if lit, ok := c.Args[0].(*ast.BasicLit); ok && lit.Value == `"MINICLOUD_TEST_DATABASE_URL"` {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

func hasNameGuard(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return !found
		}
		switch fun := c.Fun.(type) {
		case *ast.Ident:
			if fun.Name == "isTestDatabaseName" || fun.Name == "setupTestSchema" || fun.Name == "setupTestPool" {
				found = true
			}
		case *ast.SelectorExpr:
			if fun.Sel.Name == "HasSuffix" && len(c.Args) == 2 {
				if lit, ok := c.Args[1].(*ast.BasicLit); ok && lit.Value == `"_test"` {
					found = true
				}
			}
		}
		return !found
	})
	return found
}
