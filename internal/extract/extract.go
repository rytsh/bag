// Package extract runs per-file extractors and the corpus-level resolution
// passes (id canonicalization, collision disambiguation, stub rewiring and
// cross-file call resolution).
//
// Adapted from Graphify's graphify/extract.py (Apache-2.0).
package extract

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/model"
)

// Extractor extracts one file. path is absolute, root is the scan root and
// src is the file content.
type Extractor func(path, root string, src []byte) *model.Extraction

// Options configure an extraction run.
type Options struct {
	Root    string
	Workers int
	// Cache, when set, is consulted/filled per file.
	Cache Cache
	// Progress is called after each file.
	Progress func(done, total int, path string)
}

// Cache stores per-file extraction results keyed by content.
type Cache interface {
	Get(path string, src []byte) (*model.Extraction, bool)
	Put(path string, src []byte, ex *model.Extraction)
}

// Result is the merged, resolved extraction.
type Result struct {
	Nodes         []*model.Node
	Edges         []*model.Edge
	Failed        []string
	Extracted     []string
	NoExtractor   map[string]int
	SyntaxErrored []string
}

type fileResult struct {
	path string
	ex   *model.Extraction
}

// Run extracts paths and resolves cross-file relations.
func Run(ctx context.Context, paths []string, opt Options) (*Result, error) {
	root, err := filepath.Abs(opt.Root)
	if err != nil {
		return nil, err
	}

	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}

	workers := opt.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	abs := make([]string, len(paths))
	for i, p := range paths {
		a, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}

		abs[i] = a
	}

	per := make([]fileResult, len(abs))
	res := &Result{NoExtractor: map[string]int{}}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		done int
	)

	jobs := make(chan int)

	for range workers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for i := range jobs {
				per[i] = fileResult{path: abs[i], ex: extractOne(abs[i], root, opt.Cache)}

				if opt.Progress != nil {
					mu.Lock()
					done++
					opt.Progress(done, len(abs), abs[i])
					mu.Unlock()
				}
			}
		}()
	}

	for i := range abs {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()

			return nil, ctx.Err()
		case jobs <- i:
		}
	}

	close(jobs)
	wg.Wait()

	for _, fr := range per {
		if fr.ex == nil {
			res.NoExtractor[strings.ToLower(filepath.Ext(fr.path))]++

			continue
		}

		if fr.ex.Skipped {
			continue
		}

		if fr.ex.Error != "" || len(fr.ex.Nodes) == 0 {
			res.Failed = append(res.Failed, fr.path)
		} else {
			res.Extracted = append(res.Extracted, fr.path)
		}

		if fr.ex.ParseErrors && len(fr.ex.Nodes) <= 1 {
			res.SyntaxErrored = append(res.SyntaxErrored, fr.path)
		}
	}

	resolve(root, per, res)

	return res, nil
}

func extractOne(path, root string, cache Cache) *model.Extraction {
	ext := Lookup(path)
	if ext == nil {
		return nil
	}

	src, err := os.ReadFile(path)
	if err != nil {
		return &model.Extraction{Error: err.Error()}
	}

	if cache != nil {
		if cached, ok := cache.Get(path, src); ok {
			return cached
		}
	}

	ex := safeExtract(ext, path, root, src)
	if cache != nil && ex.Error == "" && len(ex.Nodes) > 0 {
		cache.Put(path, src, ex)
	}

	return ex
}

func safeExtract(ext Extractor, path, root string, src []byte) (out *model.Extraction) {
	defer func() {
		if r := recover(); r != nil {
			out = &model.Extraction{Error: "panic during extraction"}
		}
	}()

	out = ext(path, root, src)
	if out == nil {
		out = &model.Extraction{}
	}

	return out
}

// relPath returns p relative to root with forward slashes, or "" when p is
// outside root.
func relPath(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}

	return filepath.ToSlash(rel)
}

var _ = base.MakeID

func isRegularFile(p string) bool {
	st, err := os.Stat(p)

	return err == nil && st.Mode().IsRegular()
}
