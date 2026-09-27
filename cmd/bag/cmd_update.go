package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/rytsh/bag/internal/config"
	"github.com/rytsh/bag/internal/detect"
	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/pipeline"
	"github.com/rytsh/bag/internal/store"
)

func init() {
	register(command{name: "update", usage: "rebuild the graph reusing the AST cache (fast incremental)", run: runUpdate})
	register(command{name: "watch", usage: "rebuild the graph whenever source files change", run: runWatch})
	register(command{name: "hook", usage: "install|uninstall|status git hooks that keep the graph fresh", run: runHook})
}

func buildOptions(ctx context.Context, root string) (pipeline.Options, error) {
	cfg := config.From(ctx)

	abs, err := filepath.Abs(root)
	if err != nil {
		return pipeline.Options{}, err
	}

	out := pipeline.OutDir(abs, cfg.OutDir)
	opt := pipeline.Options{Root: abs, OutDir: out, Gitignore: true, Workers: cfg.Workers, Resolution: 1.0, Force: true}

	if c, err := store.OpenCache(filepath.Join(out, "cache")); err == nil {
		opt.Cache = c
	}

	return opt, nil
}

func runUpdate(ctx context.Context, args []string) error {
	fs := newFlags("update")
	noViz := fs.Bool("no-viz", false, "skip graph.html")

	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}

	root := "."
	if len(pos) > 0 {
		root = pos[0]
	}

	opt, err := buildOptions(ctx, root)
	if err != nil {
		return err
	}

	opt.NoViz = *noViz

	start := time.Now()

	res, err := pipeline.Run(ctx, opt)
	if err != nil {
		return err
	}

	fmt.Printf("[bag update] %d nodes, %d edges, %d communities in %s\n",
		res.Graph.NumNodes(), res.Graph.NumEdges(), len(res.Communities), time.Since(start).Round(time.Millisecond))

	return nil
}

func runWatch(ctx context.Context, args []string) error {
	fset := newFlags("watch")
	debounce := fset.Duration("debounce", 3*time.Second, "quiet period before rebuilding")

	pos, err := parseInterspersed(fset, args)
	if err != nil {
		return err
	}

	root := "."
	if len(pos) > 0 {
		root = pos[0]
	}

	opt, err := buildOptions(ctx, root)
	if err != nil {
		return err
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	addDirs := func() {
		_ = filepath.WalkDir(opt.Root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil //nolint:nilerr // best effort
			}

			name := d.Name()
			if p != opt.Root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" ||
				p == opt.OutDir || name == detect.DefaultOutDir) {
				return filepath.SkipDir
			}

			_ = w.Add(p)

			return nil
		})
	}

	addDirs()

	rebuild := func() {
		start := time.Now()

		res, err := pipeline.Run(ctx, opt)
		if err != nil {
			slog.Error("rebuild failed", "error", err)

			return
		}

		slog.Info("graph rebuilt", "nodes", res.Graph.NumNodes(), "edges", res.Graph.NumEdges(),
			"took", time.Since(start).Round(time.Millisecond))
	}

	rebuild()
	slog.Info("watching for changes", "root", opt.Root)

	var timer *time.Timer

	fire := make(chan struct{}, 1)

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}

			if strings.HasPrefix(ev.Name, opt.OutDir) {
				continue
			}

			if ev.Op&fsnotify.Create != 0 {
				if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
					addDirs()
				}
			}

			if !extract.IsCode(ev.Name) && !isDocLike(ev.Name) {
				continue
			}

			if timer != nil {
				timer.Stop()
			}

			timer = time.AfterFunc(*debounce, func() {
				select {
				case fire <- struct{}{}:
				default:
				}
			})
		case <-fire:
			rebuild()
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}

			slog.Warn("watch error", "error", err)
		}
	}
}

func isDocLike(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))

	return detect.DocExtensions[ext] || detect.PaperExtensions[ext]
}

const (
	hookBegin = "# >>> bag hook >>>"
	hookEnd   = "# <<< bag hook <<<"
)

func hookBody(bin string) string {
	return fmt.Sprintf(`%s
if [ -z "$BAG_SKIP_HOOK" ]; then
  root="$(git rev-parse --show-toplevel 2>/dev/null)"
  if [ -n "$root" ]; then
    ( cd "$root" && %q update . --no-viz >/dev/null 2>&1 & )
  fi
fi
%s
`, hookBegin, bin, hookEnd)
}

func runHook(_ context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: bag hook install|uninstall|status")
	}

	out, err := exec.Command("git", "rev-parse", "--git-path", "hooks").Output()
	if err != nil {
		return errors.New("not inside a git repository")
	}

	hooksDir, _ := filepath.Abs(strings.TrimSpace(string(out)))

	bin, err := os.Executable()
	if err != nil {
		bin = "bag"
	}

	hooks := []string{"post-commit", "post-checkout", "post-merge"}

	switch args[0] {
	case "install":
		if err := os.MkdirAll(hooksDir, 0o755); err != nil {
			return err
		}

		for _, h := range hooks {
			p := filepath.Join(hooksDir, h)

			existing, _ := os.ReadFile(p)
			content := stripHook(string(existing))

			if strings.TrimSpace(content) == "" {
				content = "#!/bin/sh\n"
			}

			content = strings.TrimRight(content, "\n") + "\n" + hookBody(bin)

			if err := os.WriteFile(p, []byte(content), 0o755); err != nil { //nolint:gosec // hooks must be executable
				return err
			}

			fmt.Printf("installed %s\n", p)
		}
	case "uninstall":
		for _, h := range hooks {
			p := filepath.Join(hooksDir, h)

			existing, err := os.ReadFile(p)
			if err != nil {
				continue
			}

			content := stripHook(string(existing))
			if strings.TrimSpace(content) == "#!/bin/sh" || strings.TrimSpace(content) == "" {
				_ = os.Remove(p)
			} else if err := os.WriteFile(p, []byte(content), 0o755); err != nil { //nolint:gosec // hooks must be executable
				return err
			}

			fmt.Printf("removed bag hook from %s\n", p)
		}
	case "status":
		for _, h := range hooks {
			raw, _ := os.ReadFile(filepath.Join(hooksDir, h))

			state := "not installed"
			if strings.Contains(string(raw), hookBegin) {
				state = "installed"
			}

			fmt.Printf("%-14s %s\n", h, state)
		}
	default:
		return fmt.Errorf("unknown hook action %q", args[0])
	}

	return nil
}

func stripHook(s string) string {
	for {
		i := strings.Index(s, hookBegin)
		if i < 0 {
			return s
		}

		j := strings.Index(s[i:], hookEnd)
		if j < 0 {
			return s[:i]
		}

		s = s[:i] + strings.TrimLeft(s[i+j+len(hookEnd):], "\n")
	}
}
