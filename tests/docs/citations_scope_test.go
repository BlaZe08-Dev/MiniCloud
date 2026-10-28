package docs_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// brokenLinkDoc quotes a README-style link that is only valid relative to the repo root.
const brokenLinkDoc = "See [state](docs/PROJECT_STATE.md).\n"

func scopeFS(extra fstest.MapFS) fstest.MapFS {
	m := fstest.MapFS{"docs/PROJECT_STATE.md": &fstest.MapFile{Data: []byte("# state\n")}}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func ruleAFiles(fsys fs.FS) map[string]bool {
	al, _ := ParseAllowlist("")
	got := map[string]bool{}
	for _, v := range CheckRuleA(fsys, al) {
		got[v.File] = true
	}
	return got
}

// TestScope_LiveDocBrokenLinkStillFails guards that live documents keep the full Rule A.
func TestScope_LiveDocBrokenLinkStillFails(t *testing.T) {
	fsys := scopeFS(fstest.MapFS{
		"docs/guides/live.md": &fstest.MapFile{Data: []byte(brokenLinkDoc)},
	})
	if !ruleAFiles(fsys)["docs/guides/live.md"] {
		t.Fatal("broken link in a live document must fail Rule A")
	}
}

// TestScope_ReviewRecordBrokenLinkSkipped guards the designed behaviour: link targets in
// frozen review records are not checked, but code-span paths still are.
func TestScope_ReviewRecordBrokenLinkSkipped(t *testing.T) {
	fsys := scopeFS(fstest.MapFS{
		"docs/agents/reviews/R.md": &fstest.MapFile{Data: []byte(brokenLinkDoc)},
		"docs/agents/reviews/S.md": &fstest.MapFile{Data: []byte("Cites `internal/missing/file.go`.\n")},
		"docs/agents/reviews/T.md": &fstest.MapFile{Data: []byte("Cites `docs/PROJECT_STATE.md` and ADR-0099.\n")},
	})
	got := ruleAFiles(fsys)
	if got["docs/agents/reviews/R.md"] {
		t.Error("broken link in a frozen review record must not fail Rule A")
	}
	if !got["docs/agents/reviews/S.md"] {
		t.Error("missing code-span path in a review record must still fail Rule A")
	}
	if got["docs/agents/reviews/T.md"] {
		t.Error("existing code-span path in a review record must pass Rule A")
	}
	al, _ := ParseAllowlist("")
	if len(CheckRuleE(fsys, al)) == 0 {
		t.Error("Rule E must still apply to review records")
	}
}

// TestScope_LookalikePathsAreLive guards that prefix, traversal and case tricks do not
// reach the frozen scope.
func TestScope_LookalikePathsAreLive(t *testing.T) {
	fsys := scopeFS(fstest.MapFS{
		"docs/agents/reviews-old/x.md": &fstest.MapFile{Data: []byte(brokenLinkDoc)},
		"docs/agents/reviews.md":       &fstest.MapFile{Data: []byte(brokenLinkDoc)},
		"docs/agents/Reviews/x.md":     &fstest.MapFile{Data: []byte(brokenLinkDoc)},
		"docs/agents/x.md":             &fstest.MapFile{Data: []byte(brokenLinkDoc)},
	})
	got := ruleAFiles(fsys)
	for _, f := range []string{"docs/agents/reviews-old/x.md", "docs/agents/reviews.md", "docs/agents/Reviews/x.md", "docs/agents/x.md"} {
		if !got[f] {
			t.Errorf("%s must be treated as a live document", f)
		}
	}
	for _, p := range []string{
		"docs/agents/reviews/../x.md",
		"docs/agents/reviews/./x.md",
		"docs/agents/reviews//x.md",
		"docs/agents/reviews/",
		"docs/agents/reviews",
		"/docs/agents/reviews/x.md",
		"../docs/agents/reviews/x.md",
	} {
		if IsFrozenReviewRecord(fsys, p) {
			t.Errorf("%q must not be classified as a frozen review record", p)
		}
	}
	fsys["docs/agents/reviews/real.md"] = &fstest.MapFile{Data: []byte("x\n")}
	if !IsFrozenReviewRecord(fsys, "docs/agents/reviews/real.md") {
		t.Error("an existing file inside the directory must be frozen")
	}
	if IsFrozenReviewRecord(fsys, "docs/agents/reviews/missing.md") {
		t.Error("a nonexistent file must not be classified as frozen")
	}
}

// TestScope_SymlinksCannotSmuggleIntoScope uses a real directory because symlinks need one.
func TestScope_SymlinksCannotSmuggleIntoScope(t *testing.T) {
	root := t.TempDir()
	mk := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, rel string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	mk("docs/PROJECT_STATE.md", "# state\n")
	mk("docs/guides/live.md", brokenLinkDoc)
	mk("docs/agents/reviews/real.md", brokenLinkDoc)
	// file symlink inside the scope pointing at a live document
	link("../../guides/live.md", "docs/agents/reviews/smuggled.md")
	// directory symlinks: reviews itself, and a subdirectory of it, pointing at live dirs
	link("guides", "docs/agents/other-reviews")
	link("../../guides", "docs/agents/reviews/sub")

	fsys := os.DirFS(root)
	if !IsFrozenReviewRecord(fsys, "docs/agents/reviews/real.md") {
		t.Fatal("regular file in the scope must be frozen")
	}
	for _, p := range []string{"docs/agents/reviews/smuggled.md", "docs/agents/reviews/sub/live.md"} {
		if IsFrozenReviewRecord(fsys, p) {
			t.Errorf("%s goes through a symlink and must not be frozen", p)
		}
	}
	got := ruleAFiles(fsys)
	if !got["docs/agents/reviews/smuggled.md"] {
		t.Error("symlinked live document inside the scope must still fail Rule A")
	}
	if !got["docs/guides/live.md"] {
		t.Error("the live document itself must fail Rule A")
	}
	if got["docs/agents/reviews/real.md"] {
		t.Error("the regular record must stay exempt from the link check")
	}

	// the scope directory itself replaced by a symlink to a live directory
	root2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root2, "docs", "agents", "elsewhere"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root2, "docs", "agents", "elsewhere", "x.md"), []byte(brokenLinkDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", filepath.Join(root2, "docs", "agents", "reviews")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if IsFrozenReviewRecord(os.DirFS(root2), "docs/agents/reviews/x.md") {
		t.Error("a symlinked reviews directory must not scope its target")
	}
}
