package docs_test

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// AllowlistEntry represents a single entry in citation_allowlist.txt.
type AllowlistEntry struct {
	Kind   string
	Value  string
	Reason string
	Line   int
}

// Allowlist indexes suppression entries by kind.
type Allowlist struct {
	Entries []AllowlistEntry
	Paths   map[string]bool
	Tests   map[string]bool
	Strings map[string]bool
}

// Violation records a failure of any citation rule.
type Violation struct {
	Rule    string
	File    string
	Line    int
	Message string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s:%d: [%s] %s", v.File, v.Line, v.Rule, v.Message)
}

var (
	linkRe      = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)
	codeSpanRe  = regexp.MustCompile("`([^`]+)`")
	repoPathRe  = regexp.MustCompile(`(?:\./|\.\./)*(?:internal|cmd|tests|deployments|docs|migrations)/[a-zA-Z0-9_./-]+\.(?:go|md|yml|sql)`)
	colonLineRe = regexp.MustCompile(`([a-zA-Z0-9_./-]+\.(?:go|md|yml|sql)):([0-9]+)(?:-([0-9]+))?`)
	hashLineRe  = regexp.MustCompile(`([a-zA-Z0-9_./-]+\.(?:go|md|yml|sql))#L([0-9]+)(?:-L([0-9]+))?`)
	anchorRe    = regexp.MustCompile(`^L([0-9]+)(?:-L([0-9]+))?$`)
	testIdentRe = regexp.MustCompile(`\bTest[A-Z][A-Za-z0-9_]*`)
	adrRe       = regexp.MustCompile(`\bADR[- ]?0*([1-9][0-9]*)\b`)

	exemptWords = []string{
		"proposed",
		"future",
		"planned",
		"designed, not executed",
		"does not exist",
		"nonexistent",
		"invented",
	}
)

// ParseAllowlist parses the contents of an allowlist file.
func ParseAllowlist(content string) (Allowlist, []Violation) {
	al := Allowlist{
		Paths:   make(map[string]bool),
		Tests:   make(map[string]bool),
		Strings: make(map[string]bool),
	}
	var violations []Violation
	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			violations = append(violations, Violation{
				Rule:    "RuleF",
				File:    "tests/docs/citation_allowlist.txt",
				Line:    lineNum,
				Message: fmt.Sprintf("invalid allowlist format, expected kind|value|reason, got %q", line),
			})
			continue
		}
		kind := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		reason := strings.TrimSpace(parts[2])

		if kind != "path" && kind != "test" && kind != "string" {
			violations = append(violations, Violation{
				Rule:    "RuleF",
				File:    "tests/docs/citation_allowlist.txt",
				Line:    lineNum,
				Message: fmt.Sprintf("invalid allowlist kind %q (must be path, test, or string)", kind),
			})
			continue
		}
		if value == "" {
			violations = append(violations, Violation{
				Rule:    "RuleF",
				File:    "tests/docs/citation_allowlist.txt",
				Line:    lineNum,
				Message: "allowlist value cannot be empty",
			})
			continue
		}
		if len(reason) < 15 {
			violations = append(violations, Violation{
				Rule:    "RuleF",
				File:    "tests/docs/citation_allowlist.txt",
				Line:    lineNum,
				Message: fmt.Sprintf("allowlist reason must be at least 15 characters, got %d (%q)", len(reason), reason),
			})
			continue
		}

		entry := AllowlistEntry{Kind: kind, Value: value, Reason: reason, Line: lineNum}
		al.Entries = append(al.Entries, entry)
		switch kind {
		case "path":
			al.Paths[value] = true
		case "test":
			al.Tests[value] = true
		case "string":
			al.Strings[value] = true
		}
	}
	return al, violations
}

// CollectRealTests inspects all *_test.go files using go/parser and returns defined test function names.
func CollectRealTests(fsys fs.FS) (map[string]bool, error) {
	testNames := make(map[string]bool)
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), "_test.go") {
			content, err := fs.ReadFile(fsys, path)
			if err != nil {
				return err
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, content, 0)
			if err != nil {
				return err
			}
			for _, decl := range f.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
					if strings.HasPrefix(fn.Name.Name, "Test") {
						testNames[fn.Name.Name] = true
					}
				}
			}
		}
		return nil
	})
	return testNames, err
}

// CollectDocFiles gathers all markdown documents under docs/ (excluding docs/source/) plus README.md, GEMINI.md, and CHANGELOG.md.
func CollectDocFiles(fsys fs.FS) ([]string, error) {
	var docFiles []string
	for _, rootFile := range []string{"README.md", "GEMINI.md", "CHANGELOG.md"} {
		if _, err := fs.Stat(fsys, rootFile); err == nil {
			docFiles = append(docFiles, rootFile)
		}
	}
	if _, err := fs.Stat(fsys, "docs"); err == nil {
		err := fs.WalkDir(fsys, "docs", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(path)
			if d.IsDir() {
				if rel == "docs/source" || strings.HasPrefix(rel, "docs/source/") {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(rel, "docs/source/") {
				return nil
			}
			if strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
				docFiles = append(docFiles, rel)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return docFiles, nil
}

// CollectFileLineCounts records total lines for every file in the repository.
func CollectFileLineCounts(fsys fs.FS) (map[string]int, error) {
	counts := make(map[string]int)
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		content, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil
		}
		rel := filepath.ToSlash(path)
		lineCount := bytes.Count(content, []byte{'\n'})
		if len(content) > 0 && !bytes.HasSuffix(content, []byte{'\n'}) {
			lineCount++
		}
		counts[rel] = lineCount
		return nil
	})
	return counts, err
}

func fileExists(fsys fs.FS, path string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	clean = strings.TrimPrefix(clean, "./")
	_, err := fs.Stat(fsys, clean)
	return err == nil
}

// CheckRuleA validates that every cited repo path exists.
func CheckRuleA(fsys fs.FS, allowlist Allowlist) []Violation {
	docFiles, err := CollectDocFiles(fsys)
	if err != nil {
		return []Violation{{Rule: "RuleA", File: ".", Line: 1, Message: err.Error()}}
	}
	var violations []Violation

	for _, docRel := range docFiles {
		docDir := filepath.Dir(docRel)
		content, err := fs.ReadFile(fsys, docRel)
		if err != nil {
			continue
		}
		lines := strings.Split(string(content), "\n")
		for i, line := range lines {
			lineNum := i + 1

			// 1. Markdown link targets: [text](target)
			for _, m := range linkRe.FindAllStringSubmatch(line, -1) {
				target := m[2]
				if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") {
					continue
				}
				if strings.HasPrefix(target, "#") {
					continue
				}
				targetPath := strings.Split(target, "#")[0]
				targetPath = strings.Split(targetPath, "?")[0]
				if targetPath == "" {
					continue
				}
				if allowlist.Paths[targetPath] {
					continue
				}

				var resolved string
				if strings.HasPrefix(targetPath, "/") {
					resolved = filepath.ToSlash(filepath.Clean(strings.TrimPrefix(targetPath, "/")))
				} else {
					resolved = filepath.ToSlash(filepath.Clean(filepath.Join(docDir, targetPath)))
				}
				if allowlist.Paths[resolved] {
					continue
				}
				if !fileExists(fsys, resolved) {
					violations = append(violations, Violation{
						Rule:    "RuleA",
						File:    docRel,
						Line:    lineNum,
						Message: fmt.Sprintf("cited path %q does not exist (resolved to %q)", targetPath, resolved),
					})
				}
			}

			// 2. Repo paths in code spans: `internal/...`, etc.
			for _, m := range codeSpanRe.FindAllStringSubmatch(line, -1) {
				span := m[1]
				for _, match := range repoPathRe.FindAllString(span, -1) {
					pathOnly := strings.Split(match, "#")[0]
					pathOnly = strings.Split(pathOnly, ":")[0]
					if allowlist.Paths[pathOnly] {
						continue
					}
					cleanRoot := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(pathOnly, "./")))
					if fileExists(fsys, cleanRoot) {
						continue
					}
					cleanRel := filepath.ToSlash(filepath.Clean(filepath.Join(docDir, pathOnly)))
					if fileExists(fsys, cleanRel) {
						continue
					}
					if allowlist.Paths[cleanRoot] || allowlist.Paths[cleanRel] {
						continue
					}
					violations = append(violations, Violation{
						Rule:    "RuleA",
						File:    docRel,
						Line:    lineNum,
						Message: fmt.Sprintf("cited path %q does not exist in repository", pathOnly),
					})
				}
			}
		}
	}
	return violations
}

// CheckRuleB validates line numbers and line anchor bounds on existing files.
func CheckRuleB(fsys fs.FS, allowlist Allowlist) []Violation {
	docFiles, err := CollectDocFiles(fsys)
	if err != nil {
		return []Violation{{Rule: "RuleB", File: ".", Line: 1, Message: err.Error()}}
	}
	lineCounts, err := CollectFileLineCounts(fsys)
	if err != nil {
		return []Violation{{Rule: "RuleB", File: ".", Line: 1, Message: err.Error()}}
	}
	var violations []Violation

	for _, docRel := range docFiles {
		docDir := filepath.Dir(docRel)
		content, err := fs.ReadFile(fsys, docRel)
		if err != nil {
			continue
		}
		lines := strings.Split(string(content), "\n")
		for i, line := range lines {
			lineNum := i + 1

			// 1. Markdown link anchors: target#L123 or target#L10-L20
			for _, m := range linkRe.FindAllStringSubmatch(line, -1) {
				target := m[2]
				if !strings.Contains(target, "#L") {
					continue
				}
				parts := strings.SplitN(target, "#", 2)
				targetPath := parts[0]
				anchor := parts[1]
				var resolved string
				if strings.HasPrefix(targetPath, "/") {
					resolved = filepath.ToSlash(filepath.Clean(strings.TrimPrefix(targetPath, "/")))
				} else {
					resolved = filepath.ToSlash(filepath.Clean(filepath.Join(docDir, targetPath)))
				}
				if allowlist.Paths[targetPath] || allowlist.Paths[resolved] {
					continue
				}
				if totalLines, exists := lineCounts[resolved]; exists {
					match := anchorRe.FindStringSubmatch(anchor)
					if match != nil {
						start, _ := strconv.Atoi(match[1])
						end := start
						if match[2] != "" {
							end, _ = strconv.Atoi(match[2])
						}
						if start > totalLines || end > totalLines {
							violations = append(violations, Violation{
								Rule:    "RuleB",
								File:    docRel,
								Line:    lineNum,
								Message: fmt.Sprintf("anchor #%s in %s exceeds file line count (%d)", anchor, resolved, totalLines),
							})
						}
					}
				}
			}

			// 2. Colon line references in code spans or text: path:123 or path:10-20
			for _, m := range colonLineRe.FindAllStringSubmatch(line, -1) {
				rawPath := m[1]
				startStr := m[2]
				endStr := m[3]

				var resolved string
				cleanRoot := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(rawPath, "./")))
				if _, ok := lineCounts[cleanRoot]; ok {
					resolved = cleanRoot
				} else {
					cleanRel := filepath.ToSlash(filepath.Clean(filepath.Join(docDir, rawPath)))
					if _, ok := lineCounts[cleanRel]; ok {
						resolved = cleanRel
					}
				}
				if resolved == "" {
					continue // path does not exist, checked by Rule A
				}
				if allowlist.Paths[rawPath] || allowlist.Paths[resolved] {
					continue
				}

				totalLines := lineCounts[resolved]
				start, _ := strconv.Atoi(startStr)
				end := start
				if endStr != "" {
					end, _ = strconv.Atoi(endStr)
				}
				if start > totalLines || end > totalLines {
					violations = append(violations, Violation{
						Rule:    "RuleB",
						File:    docRel,
						Line:    lineNum,
						Message: fmt.Sprintf("line citation %s:%s exceeds file line count (%d)", resolved, startStr, totalLines),
					})
				}
			}

			// 3. Hash line references in code spans or text: path#L123 or path#L10-L20
			for _, m := range hashLineRe.FindAllStringSubmatch(line, -1) {
				rawPath := m[1]
				startStr := m[2]
				endStr := m[3]

				var resolved string
				cleanRoot := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(rawPath, "./")))
				if _, ok := lineCounts[cleanRoot]; ok {
					resolved = cleanRoot
				} else {
					cleanRel := filepath.ToSlash(filepath.Clean(filepath.Join(docDir, rawPath)))
					if _, ok := lineCounts[cleanRel]; ok {
						resolved = cleanRel
					}
				}
				if resolved == "" {
					continue
				}
				if allowlist.Paths[rawPath] || allowlist.Paths[resolved] {
					continue
				}

				totalLines := lineCounts[resolved]
				start, _ := strconv.Atoi(startStr)
				end := start
				if endStr != "" {
					end, _ = strconv.Atoi(endStr)
				}
				if start > totalLines || end > totalLines {
					violations = append(violations, Violation{
						Rule:    "RuleB",
						File:    docRel,
						Line:    lineNum,
						Message: fmt.Sprintf("line citation %s#L%s exceeds file line count (%d)", resolved, startStr, totalLines),
					})
				}
			}
		}
	}
	return violations
}

// CheckRuleC validates that every cited test identifier exists in the test suite unless exempted on the line.
func CheckRuleC(fsys fs.FS, allowlist Allowlist) []Violation {
	realTests, err := CollectRealTests(fsys)
	if err != nil {
		return []Violation{{Rule: "RuleC", File: ".", Line: 1, Message: err.Error()}}
	}
	docFiles, err := CollectDocFiles(fsys)
	if err != nil {
		return []Violation{{Rule: "RuleC", File: ".", Line: 1, Message: err.Error()}}
	}
	var violations []Violation

	for _, docRel := range docFiles {
		content, err := fs.ReadFile(fsys, docRel)
		if err != nil {
			continue
		}
		lines := strings.Split(string(content), "\n")
		for i, line := range lines {
			lineNum := i + 1
			matches := testIdentRe.FindAllString(line, -1)
			if len(matches) == 0 {
				continue
			}

			lineWithoutTests := testIdentRe.ReplaceAllString(line, "")
			lowerLine := strings.ToLower(lineWithoutTests)
			exempt := false
			for _, ew := range exemptWords {
				if strings.Contains(lowerLine, ew) {
					exempt = true
					break
				}
			}
			if exempt {
				continue
			}

			for _, m := range matches {
				topLevel := strings.Split(m, "/")[0]
				if allowlist.Tests[topLevel] {
					continue
				}
				if !realTests[topLevel] {
					violations = append(violations, Violation{
						Rule:    "RuleC",
						File:    docRel,
						Line:    lineNum,
						Message: fmt.Sprintf("cited test %q does not exist", topLevel),
					})
				}
			}
		}
	}
	return violations
}

// CheckRuleD asserts forbidden strings are never present in scanned docs.
func CheckRuleD(fsys fs.FS, allowlist Allowlist) []Violation {
	docFiles, err := CollectDocFiles(fsys)
	if err != nil {
		return []Violation{{Rule: "RuleD", File: ".", Line: 1, Message: err.Error()}}
	}
	var violations []Violation

	for _, docRel := range docFiles {
		content, err := fs.ReadFile(fsys, docRel)
		if err != nil {
			continue
		}
		lines := strings.Split(string(content), "\n")
		for i, line := range lines {
			lineNum := i + 1
			if strings.Contains(line, "file:///") && !allowlist.Strings["file:///"] {
				violations = append(violations, Violation{
					Rule:    "RuleD",
					File:    docRel,
					Line:    lineNum,
					Message: `forbidden string "file:///" found`,
				})
			}
			if strings.Contains(line, "/api/v1") && !allowlist.Strings["/api/v1"] {
				violations = append(violations, Violation{
					Rule:    "RuleD",
					File:    docRel,
					Line:    lineNum,
					Message: `forbidden string "/api/v1" found`,
				})
			}
		}
	}
	return violations
}

// CheckRuleE asserts that every cited ADR exists under docs/adr/00NN-*.md.
func CheckRuleE(fsys fs.FS, allowlist Allowlist) []Violation {
	docFiles, err := CollectDocFiles(fsys)
	if err != nil {
		return []Violation{{Rule: "RuleE", File: ".", Line: 1, Message: err.Error()}}
	}
	var violations []Violation

	adrNumbers := make(map[int]bool)
	if _, err := fs.Stat(fsys, "docs/adr"); err == nil {
		entries, _ := fs.ReadDir(fsys, "docs/adr")
		adrFileRe := regexp.MustCompile(`^0*([0-9]{1,4})-.*\.md$`)
		for _, e := range entries {
			if m := adrFileRe.FindStringSubmatch(e.Name()); m != nil {
				num, _ := strconv.Atoi(m[1])
				adrNumbers[num] = true
			}
		}
	}

	for _, docRel := range docFiles {
		content, err := fs.ReadFile(fsys, docRel)
		if err != nil {
			continue
		}
		lines := strings.Split(string(content), "\n")
		for i, line := range lines {
			lineNum := i + 1
			matches := adrRe.FindAllStringSubmatch(line, -1)
			for _, m := range matches {
				num, _ := strconv.Atoi(m[1])
				if !adrNumbers[num] {
					violations = append(violations, Violation{
						Rule:    "RuleE",
						File:    docRel,
						Line:    lineNum,
						Message: fmt.Sprintf("cited ADR %d does not exist (expected docs/adr/%04d-*.md)", num, num),
					})
				}
			}
		}
	}
	return violations
}

// CheckRuleF validates allowlist entries for syntax, reason length, and staleness.
func CheckRuleF(fsys fs.FS, allowlist Allowlist) []Violation {
	var violations []Violation
	docFiles, err := CollectDocFiles(fsys)
	if err != nil {
		return []Violation{{Rule: "RuleF", File: "tests/docs/citation_allowlist.txt", Line: 1, Message: err.Error()}}
	}
	realTests, _ := CollectRealTests(fsys)

	var combinedDocs strings.Builder
	for _, docRel := range docFiles {
		content, err := fs.ReadFile(fsys, docRel)
		if err == nil {
			combinedDocs.Write(content)
			combinedDocs.WriteString("\n")
		}
	}
	allDocsStr := combinedDocs.String()

	for _, entry := range allowlist.Entries {
		// 1. Is it cited in any scanned doc?
		if !strings.Contains(allDocsStr, entry.Value) {
			violations = append(violations, Violation{
				Rule:    "RuleF",
				File:    "tests/docs/citation_allowlist.txt",
				Line:    entry.Line,
				Message: fmt.Sprintf("allowlist entry is stale: %s %q is not cited in any scanned document", entry.Kind, entry.Value),
			})
			continue
		}

		// 2. Does it now exist?
		switch entry.Kind {
		case "path":
			if fileExists(fsys, entry.Value) {
				violations = append(violations, Violation{
					Rule:    "RuleF",
					File:    "tests/docs/citation_allowlist.txt",
					Line:    entry.Line,
					Message: fmt.Sprintf("allowlist entry is stale: path %q now exists in repository", entry.Value),
				})
			}
		case "test":
			if realTests[entry.Value] {
				violations = append(violations, Violation{
					Rule:    "RuleF",
					File:    "tests/docs/citation_allowlist.txt",
					Line:    entry.Line,
					Message: fmt.Sprintf("allowlist entry is stale: test %q now exists in test suite", entry.Value),
				})
			}
		}
	}
	return violations
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repo root (no go.mod in parents of %s)", dir)
		}
		dir = parent
	}
}

// TestCitations executes the full suite of citation integrity rules against the repository.
func TestCitations(t *testing.T) {
	repoRoot := findRepoRoot(t)
	repoFS := os.DirFS(repoRoot)

	allowlistBytes, err := fs.ReadFile(repoFS, "tests/docs/citation_allowlist.txt")
	if err != nil {
		t.Fatalf("failed to read allowlist file: %v", err)
	}
	allowlist, parseViolations := ParseAllowlist(string(allowlistBytes))
	if len(parseViolations) > 0 {
		for _, v := range parseViolations {
			t.Errorf("%s", v)
		}
	}

	t.Run("RuleA_RepoPaths", func(t *testing.T) {
		violations := CheckRuleA(repoFS, allowlist)
		for _, v := range violations {
			t.Errorf("%s", v)
		}
	})

	t.Run("RuleB_LineAnchors", func(t *testing.T) {
		violations := CheckRuleB(repoFS, allowlist)
		for _, v := range violations {
			t.Errorf("%s", v)
		}
	})

	t.Run("RuleC_TestIdentifiers", func(t *testing.T) {
		violations := CheckRuleC(repoFS, allowlist)
		for _, v := range violations {
			t.Errorf("%s", v)
		}
	})

	t.Run("RuleD_ForbiddenStrings", func(t *testing.T) {
		violations := CheckRuleD(repoFS, allowlist)
		for _, v := range violations {
			t.Errorf("%s", v)
		}
	})

	t.Run("RuleE_ADRCitations", func(t *testing.T) {
		violations := CheckRuleE(repoFS, allowlist)
		for _, v := range violations {
			t.Errorf("%s", v)
		}
	})

	t.Run("RuleF_Allowlist", func(t *testing.T) {
		violations := CheckRuleF(repoFS, allowlist)
		for _, v := range violations {
			t.Errorf("%s", v)
		}
	})
}

// TestFixtures verifies negative and positive behavior for every citation rule using pure in-memory filesystems.
func TestFixtures(t *testing.T) {
	t.Run("RuleA_RepoPaths_Pass", func(t *testing.T) {
		memFS := fstest.MapFS{
			"README.md": &fstest.MapFile{
				Data: []byte("Links to [code](internal/api/server.go) and span `internal/api/server.go`.\n"),
			},
			"internal/api/server.go": &fstest.MapFile{
				Data: []byte("package api\n"),
			},
		}
		al, _ := ParseAllowlist("")
		violations := CheckRuleA(memFS, al)
		if len(violations) != 0 {
			t.Fatalf("expected 0 violations, got: %v", violations)
		}
	})

	t.Run("RuleA_RepoPaths_Fail", func(t *testing.T) {
		memFS := fstest.MapFS{
			"README.md": &fstest.MapFile{
				Data: []byte("Cites missing file `internal/missing/file.go`.\n"),
			},
		}
		al, _ := ParseAllowlist("")
		violations := CheckRuleA(memFS, al)
		if len(violations) == 0 {
			t.Fatalf("expected violation for missing path, got none")
		}
		if !strings.Contains(violations[0].Message, "internal/missing/file.go") {
			t.Errorf("expected message to mention missing path, got %q", violations[0].Message)
		}
	})

	t.Run("RuleB_LineAnchors_Pass", func(t *testing.T) {
		memFS := fstest.MapFS{
			"README.md": &fstest.MapFile{
				Data: []byte("Cites line in span `internal/api/server.go:5` and link [link](internal/api/server.go#L3-L6).\n"),
			},
			"internal/api/server.go": &fstest.MapFile{
				Data: []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"),
			},
		}
		al, _ := ParseAllowlist("")
		violations := CheckRuleB(memFS, al)
		if len(violations) != 0 {
			t.Fatalf("expected 0 violations, got: %v", violations)
		}
	})

	t.Run("RuleB_LineAnchors_Fail", func(t *testing.T) {
		memFS := fstest.MapFS{
			"README.md": &fstest.MapFile{
				Data: []byte("Cites line out of bounds `internal/api/server.go:50`.\n"),
			},
			"internal/api/server.go": &fstest.MapFile{
				Data: []byte("1\n2\n3\n4\n5\n"),
			},
		}
		al, _ := ParseAllowlist("")
		violations := CheckRuleB(memFS, al)
		if len(violations) == 0 {
			t.Fatalf("expected violation for line beyond EOF, got none")
		}
		if !strings.Contains(violations[0].Message, "exceeds file line count") {
			t.Errorf("expected message to mention exceeds file line count, got %q", violations[0].Message)
		}
	})

	t.Run("RuleC_TestIdentifiers_Pass", func(t *testing.T) {
		memFS := fstest.MapFS{
			"docs/test.md": &fstest.MapFile{
				Data: []byte("Verified by `TestValidFunction` and `TestValidFunction/SubTest`.\n"),
			},
			"internal/api/server_test.go": &fstest.MapFile{
				Data: []byte("package api_test\nfunc TestValidFunction(t *testing.T) {}\n"),
			},
		}
		al, _ := ParseAllowlist("")
		violations := CheckRuleC(memFS, al)
		if len(violations) != 0 {
			t.Fatalf("expected 0 violations, got: %v", violations)
		}
	})

	t.Run("RuleC_TestIdentifiers_Fail", func(t *testing.T) {
		memFS := fstest.MapFS{
			"docs/test.md": &fstest.MapFile{
				Data: []byte("Cites missing test `TestDoesNotExist`.\n"),
			},
			"internal/api/server_test.go": &fstest.MapFile{
				Data: []byte("package api_test\nfunc TestValidFunction(t *testing.T) {}\n"),
			},
		}
		al, _ := ParseAllowlist("")
		violations := CheckRuleC(memFS, al)
		if len(violations) == 0 {
			t.Fatalf("expected violation for nonexistent test, got none")
		}
		if !strings.Contains(violations[0].Message, "TestDoesNotExist") {
			t.Errorf("expected message to mention TestDoesNotExist, got %q", violations[0].Message)
		}
	})

	t.Run("RuleC_ProposedExemption_Pass", func(t *testing.T) {
		memFS := fstest.MapFS{
			"docs/test.md": &fstest.MapFile{
				Data: []byte("This is a proposed test: `TestFutureEnhancement`.\nAnother one planned: `TestPlannedFeature`.\n"),
			},
			"internal/api/server_test.go": &fstest.MapFile{
				Data: []byte("package api_test\n"),
			},
		}
		al, _ := ParseAllowlist("")
		violations := CheckRuleC(memFS, al)
		if len(violations) != 0 {
			t.Fatalf("expected exemption to suppress violations, got: %v", violations)
		}
	})

	t.Run("RuleD_ForbiddenStrings_Pass", func(t *testing.T) {
		memFS := fstest.MapFS{
			"docs/test.md": &fstest.MapFile{
				Data: []byte("Endpoints are at /v1/jobs and /v1/workers.\n"),
			},
		}
		al, _ := ParseAllowlist("")
		violations := CheckRuleD(memFS, al)
		if len(violations) != 0 {
			t.Fatalf("expected 0 violations, got: %v", violations)
		}
	})

	t.Run("RuleD_ForbiddenStrings_Fail", func(t *testing.T) {
		memFS := fstest.MapFS{
			"docs/test.md": &fstest.MapFile{
				Data: []byte("Forbidden path file:///tmp/test and old route /api/v1/jobs.\n"),
			},
		}
		al, _ := ParseAllowlist("")
		violations := CheckRuleD(memFS, al)
		if len(violations) != 2 {
			t.Fatalf("expected 2 violations for forbidden strings, got %d: %v", len(violations), violations)
		}
	})

	t.Run("RuleE_ADRCitations_Pass", func(t *testing.T) {
		memFS := fstest.MapFS{
			"docs/test.md": &fstest.MapFile{
				Data: []byte("Refer to ADR 0001 and ADR-0002 for details.\n"),
			},
			"docs/adr/0001-init.md": &fstest.MapFile{
				Data: []byte("# ADR 1\n"),
			},
			"docs/adr/0002-http.md": &fstest.MapFile{
				Data: []byte("# ADR 2\n"),
			},
		}
		al, _ := ParseAllowlist("")
		violations := CheckRuleE(memFS, al)
		if len(violations) != 0 {
			t.Fatalf("expected 0 violations, got: %v", violations)
		}
	})

	t.Run("RuleE_ADRCitations_Fail", func(t *testing.T) {
		memFS := fstest.MapFS{
			"docs/test.md": &fstest.MapFile{
				Data: []byte("Refer to ADR-0099 for details.\n"),
			},
		}
		al, _ := ParseAllowlist("")
		violations := CheckRuleE(memFS, al)
		if len(violations) == 0 {
			t.Fatalf("expected violation for missing ADR, got none")
		}
		if !strings.Contains(violations[0].Message, "cited ADR 99 does not exist") {
			t.Errorf("expected message to mention ADR 99, got %q", violations[0].Message)
		}
	})

	t.Run("RuleF_Allowlist_Pass", func(t *testing.T) {
		memFS := fstest.MapFS{
			"docs/test.md": &fstest.MapFile{
				Data: []byte("Historical reference to `TestInventedName`.\n"),
			},
		}
		alText := "test|TestInventedName|Reason must be at least 15 characters long"
		al, parseViolations := ParseAllowlist(alText)
		if len(parseViolations) != 0 {
			t.Fatalf("parse allowlist failed: %v", parseViolations)
		}
		violations := CheckRuleF(memFS, al)
		if len(violations) != 0 {
			t.Fatalf("expected 0 violations, got: %v", violations)
		}
	})

	t.Run("RuleF_Allowlist_StaleFail", func(t *testing.T) {
		memFS := fstest.MapFS{
			"docs/test.md": &fstest.MapFile{
				Data: []byte("No reference to the test here.\n"),
			},
		}
		alText := "test|TestUncitedName|Reason must be at least 15 characters long"
		al, _ := ParseAllowlist(alText)
		violations := CheckRuleF(memFS, al)
		if len(violations) == 0 {
			t.Fatalf("expected stale violation for uncited entry, got none")
		}
		if !strings.Contains(violations[0].Message, "allowlist entry is stale") {
			t.Errorf("expected stale entry message, got %q", violations[0].Message)
		}
	})

	t.Run("RuleF_Allowlist_AlreadyExistsFail", func(t *testing.T) {
		memFS := fstest.MapFS{
			"docs/test.md": &fstest.MapFile{
				Data: []byte("References `TestExistingName`.\n"),
			},
			"internal/api/server_test.go": &fstest.MapFile{
				Data: []byte("package api_test\nfunc TestExistingName(t *testing.T) {}\n"),
			},
		}
		alText := "test|TestExistingName|Reason must be at least 15 characters long"
		al, _ := ParseAllowlist(alText)
		violations := CheckRuleF(memFS, al)
		if len(violations) == 0 {
			t.Fatalf("expected stale violation for existing test, got none")
		}
		if !strings.Contains(violations[0].Message, "now exists in test suite") {
			t.Errorf("expected 'now exists' message, got %q", violations[0].Message)
		}
	})

	t.Run("RuleF_Allowlist_ReasonTooShort_Fail", func(t *testing.T) {
		alText := "test|TestX|too short"
		_, violations := ParseAllowlist(alText)
		if len(violations) == 0 {
			t.Fatalf("expected violation for short reason, got none")
		}
		if !strings.Contains(violations[0].Message, "at least 15 characters") {
			t.Errorf("expected reason length violation, got %q", violations[0].Message)
		}
	})
}
