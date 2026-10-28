package integration_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envLookup(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestContainerControlAllowed(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"nothing set", map[string]string{}, false},
		{"only opt-in", map[string]string{envAllowContainerRestart: "1"}, false},
		{"only confirmation", map[string]string{envConfirmContainer: sharedContainerName}, false},
		{"opt-in not exactly 1", map[string]string{envAllowContainerRestart: "true", envConfirmContainer: sharedContainerName}, false},
		{"wrong container name", map[string]string{envAllowContainerRestart: "1", envConfirmContainer: "minicloud-other"}, false},
		{"empty container name", map[string]string{envAllowContainerRestart: "1", envConfirmContainer: ""}, false},
		{"both set", map[string]string{envAllowContainerRestart: "1", envConfirmContainer: sharedContainerName}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := containerControlAllowed(envLookup(tc.env))
			if got != tc.want {
				t.Fatalf("containerControlAllowed = %v (%s), want %v", got, why, tc.want)
			}
			if !got && why == "" {
				t.Fatal("refusal must carry a reason")
			}
		})
	}
}

// The real environment path: t.Setenv drives the same function through os.Getenv.
func TestContainerControlAllowed_RealEnvironment(t *testing.T) {
	t.Setenv(envAllowContainerRestart, "")
	t.Setenv(envConfirmContainer, "")
	if ok, _ := containerControlAllowed(os.Getenv); ok {
		t.Fatal("must be refused with both variables empty")
	}
	t.Setenv(envAllowContainerRestart, "1")
	if ok, _ := containerControlAllowed(os.Getenv); ok {
		t.Fatal("must be refused with only the opt-in set")
	}
	t.Setenv(envConfirmContainer, "wrong")
	if ok, _ := containerControlAllowed(os.Getenv); ok {
		t.Fatal("must be refused with a wrong container name")
	}
	t.Setenv(envConfirmContainer, sharedContainerName)
	if ok, why := containerControlAllowed(os.Getenv); !ok {
		t.Fatalf("must be allowed with both set: %s", why)
	}
}

// Every test function that calls the docker helpers must start with the shared guard.
// Static check (go/ast): it never executes a test.
func TestEveryContainerRestartingTestCallsTheGuard(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob test files: %v (%d files)", err, len(files))
	}
	fset := token.NewFileSet()
	seen := 0
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			if !callsAny(fn.Body, "stopPostgresContainer", "startPostgresContainer") {
				continue
			}
			seen++
			if len(fn.Body.List) == 0 || !isGuardCall(fn.Body.List[0]) {
				t.Errorf("%s in %s controls the container but its first statement is not requireContainerControl(t)", fn.Name.Name, f)
			}
		}
	}
	if seen == 0 {
		t.Fatal("found no container-controlling test; the scan itself is broken")
	}
}

func callsAny(body *ast.BlockStmt, names ...string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok {
				for _, nm := range names {
					if id.Name == nm {
						found = true
					}
				}
			}
		}
		return !found
	})
	return found
}

func isGuardCall(s ast.Stmt) bool {
	es, ok := s.(*ast.ExprStmt)
	if !ok {
		return false
	}
	c, ok := es.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := c.Fun.(*ast.Ident)
	return ok && id.Name == "requireContainerControl"
}
