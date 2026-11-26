package detect

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()

	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetect(t *testing.T) {
	root := t.TempDir()

	write(t, root, "main.go", "package main")
	write(t, root, "docs/readme.md", "# hi")
	write(t, root, "node_modules/x/index.js", "x")
	write(t, root, "gen/skip.go", "package gen")
	write(t, root, "keep/!important.go", "package keep")
	write(t, root, ".env", "SECRET=1")
	write(t, root, "id_rsa", "key")
	write(t, root, ".graphifyignore", "gen/\n")
	write(t, root, "img.png", "png")

	isCode := func(p string) bool { return filepath.Ext(p) == ".go" }

	res, err := Detect(root, Options{Gitignore: true, IsCode: isCode})
	if err != nil {
		t.Fatal(err)
	}

	rel := func(ps []string) map[string]bool {
		m := map[string]bool{}
		for _, p := range ps {
			r, _ := filepath.Rel(res.Root, p)
			m[filepath.ToSlash(r)] = true
		}

		return m
	}

	code := rel(res.Files[Code])
	if !code["main.go"] || !code["keep/!important.go"] || code["gen/skip.go"] {
		t.Errorf("code files = %v", code)
	}

	if docs := rel(res.Files[Document]); !docs["docs/readme.md"] {
		t.Errorf("docs = %v", docs)
	}

	if img := rel(res.Files[Image]); !img["img.png"] {
		t.Errorf("images = %v", img)
	}

	if len(res.Skipped) != 2 {
		t.Errorf("sensitive files skipped = %v", res.Skipped)
	}
}

func TestIgnoreNegation(t *testing.T) {
	s := &ignoreSet{}
	s.add("", "*.log")
	s.add("", "!keep.log")
	s.add("", "/build")
	s.add("", "docs/**/draft.md")

	cases := map[string]bool{
		"a.log":             true,
		"keep.log":          false,
		"sub/a.log":         true,
		"build":             true,
		"sub/build":         false,
		"docs/x/y/draft.md": true,
		"docs/draft.md":     true,
		"other/draft.md":    false,
	}

	for p, want := range cases {
		if got := s.match(p, false); got != want {
			t.Errorf("match(%q) = %v, want %v", p, got, want)
		}
	}
}

// A file link pointing outside the root is not read unless links are
// followed, and a link to a credential file is skipped whatever its name.
func TestFileSymlinksOutsideTheRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	write(t, root, "main.go", "package main")
	write(t, base, "outside/secret.txt", "s3cret")
	write(t, base, ".aws/credentials", "key")
	write(t, root, "docs/real.md", "# in root")

	for link, target := range map[string]string{
		"notes.txt": filepath.Join(base, "outside", "secret.txt"),
		"aws.md":    filepath.Join(base, ".aws", "credentials"),
		"alias.md":  filepath.Join(root, "docs", "real.md"),
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Skip("symlinks unsupported:", err)
		}
	}

	res, err := Detect(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	all := map[string]bool{}
	for _, files := range res.Files {
		for _, f := range files {
			all[filepath.Base(f)] = true
		}
	}

	if all["notes.txt"] || all["aws.md"] {
		t.Errorf("a link out of the root was read: %v", all)
	}
	if !all["alias.md"] {
		t.Errorf("a link inside the root was dropped: %v", all)
	}

	followed, err := Detect(root, Options{FollowSymlinks: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, files := range followed.Files {
		for _, f := range files {
			if filepath.Base(f) == "aws.md" {
				t.Error("a link to a credential store was read with FollowSymlinks")
			}
		}
	}
}

// The output directory is skipped even when the root is reached through a
// symlink and the output is given through it.
func TestOutDirUnderASymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	write(t, real, "main.go", "package main")
	write(t, real, "graphdocs/GRAPH_REPORT.md", "# report")

	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unsupported:", err)
	}

	res, err := Detect(link, Options{OutDir: filepath.Join(link, "graphdocs")})
	if err != nil {
		t.Fatal(err)
	}

	for _, files := range res.Files {
		for _, f := range files {
			if filepath.Base(f) == "GRAPH_REPORT.md" {
				t.Fatalf("the output directory was scanned: %s", f)
			}
		}
	}
}
