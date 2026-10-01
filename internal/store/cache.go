// Package store holds bag's on-disk caches.
package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/rytsh/bag/internal/graph"
	"github.com/rytsh/bag/internal/model"
)

// ASTVersion is bumped whenever extractor output changes shape so stale
// entries are ignored. It also identifies the linked extraction engine to
// library consumers.
const ASTVersion = "bag-ast-v7"

func init() {
	// VB.NET callable metadata retains every accepted optional-argument arity.
	gob.Register([]int{})
}

// Cache is a content-addressed per-file AST extraction cache.
type Cache struct {
	dir string
}

// OpenCache creates (if needed) and opens a cache directory.
func OpenCache(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	return &Cache{dir: dir}, nil
}

func (c *Cache) key(path string, src []byte) string {
	h := sha256.New()
	h.Write([]byte(ASTVersion))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(src)

	return hex.EncodeToString(h.Sum(nil))
}

func (c *Cache) file(k string) string {
	return filepath.Join(c.dir, k[:2], k+".gob")
}

// Get returns a cached extraction.
func (c *Cache) Get(path string, src []byte) (*model.Extraction, bool) {
	f, err := os.Open(c.file(c.key(path, src)))
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()

	var ex model.Extraction
	if err := gob.NewDecoder(f).Decode(&ex); err != nil {
		return nil, false
	}

	return &ex, true
}

// Put stores an extraction. Failures are ignored: the cache is best-effort.
func (c *Cache) Put(path string, src []byte, ex *model.Extraction) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(ex); err != nil {
		return
	}

	_ = graph.WriteFileAtomic(c.file(c.key(path, src)), buf.Bytes())
}
