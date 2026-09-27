// Package server exposes the knowledge graph over MCP (stdio or streamable
// HTTP) and a small JSON HTTP API.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rakunlabs/ada"
	mlog "github.com/rakunlabs/ada/middleware/log"
	mrecover "github.com/rakunlabs/ada/middleware/recover"
	mrequestid "github.com/rakunlabs/ada/middleware/requestid"

	"github.com/rytsh/bag/internal/analyze"
	"github.com/rytsh/bag/internal/graph"
	"github.com/rytsh/bag/internal/model"
	"github.com/rytsh/bag/internal/query"
)

// Store holds a hot-reloaded graph.
type Store struct {
	path string

	mu     sync.RWMutex
	loaded *graph.Loaded
	engine *query.Engine
	mtime  time.Time
	size   int64
}

// NewStore loads path.
func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.reload(); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Store) reload() error {
	st, err := os.Stat(s.path)
	if err != nil {
		return err
	}

	l, err := graph.Load(s.path)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.loaded, s.engine = l, query.New(l.G, l.Communities, l.Labels)
	s.mtime, s.size = st.ModTime(), st.Size()
	s.mu.Unlock()

	return nil
}

// Get returns the current graph, reloading it when the file changed.
func (s *Store) Get() (*graph.Loaded, *query.Engine) {
	if st, err := os.Stat(s.path); err == nil {
		s.mu.RLock()
		changed := !st.ModTime().Equal(s.mtime) || st.Size() != s.size
		s.mu.RUnlock()

		if changed {
			if err := s.reload(); err != nil {
				slog.Warn("graph reload failed", "error", err)
			}
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.loaded, s.engine
}

type queryArgs struct {
	Question    string `json:"question" jsonschema:"natural language question or keyword search"`
	Mode        string `json:"mode,omitempty" jsonschema:"bfs (broad context) or dfs (trace a specific path)"`
	Depth       int    `json:"depth,omitempty" jsonschema:"traversal depth (1-6), default 3"`
	TokenBudget int    `json:"token_budget,omitempty" jsonschema:"max output tokens, default 2000"`
}

type nodeArgs struct {
	Label  string `json:"label,omitempty" jsonschema:"node label or id"`
	NodeID string `json:"node_id,omitempty" jsonschema:"alias for label"`
}

type neighborArgs struct {
	Label          string `json:"label,omitempty" jsonschema:"node label or id"`
	NodeID         string `json:"node_id,omitempty" jsonschema:"alias for label"`
	RelationFilter string `json:"relation_filter,omitempty" jsonschema:"optional relation filter"`
	TokenBudget    int    `json:"token_budget,omitempty" jsonschema:"max output tokens"`
}

type communityArgs struct {
	CommunityID int `json:"community_id" jsonschema:"community id (0 = largest)"`
	TokenBudget int `json:"token_budget,omitempty" jsonschema:"max output tokens"`
}

type godArgs struct {
	TopN                  int     `json:"top_n,omitempty" jsonschema:"number of nodes, default 10"`
	ExcludeHubsPercentile float64 `json:"exclude_hubs_percentile,omitempty" jsonschema:"suppress nodes above this degree percentile"`
}

type pathArgs struct {
	Source  string `json:"source" jsonschema:"source concept"`
	Target  string `json:"target" jsonschema:"target concept"`
	MaxHops int    `json:"max_hops,omitempty" jsonschema:"maximum hops, default 8"`
}

type emptyArgs struct{}

func text(s string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}, nil, nil
}

func budget(v, def int) int {
	if v <= 0 {
		return def
	}

	return v
}

func truncate(s string, tokens int) string {
	if len(s) <= tokens*3 {
		return s
	}

	cut := s[:tokens*3]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	}

	return cut + fmt.Sprintf("\n... (truncated to ~%d token budget)", tokens)
}

// NewMCPServer builds an MCP server over store with Graphify-compatible
// tool names.
func NewMCPServer(store *Store, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "bag", Version: version}, nil)

	mcp.AddTool(s, &mcp.Tool{Name: "query_graph",
		Description: "Search the knowledge graph using BFS or DFS. Returns relevant nodes and edges as text context."},
		func(_ context.Context, _ *mcp.CallToolRequest, a queryArgs) (*mcp.CallToolResult, any, error) {
			_, e := store.Get()

			return text(e.Query(a.Question, query.Options{
				Depth: budget(a.Depth, 3), DFS: strings.EqualFold(a.Mode, "dfs"), Budget: budget(a.TokenBudget, 2000),
			}))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_node", Description: "Get full details for a specific node by label or ID."},
		func(_ context.Context, _ *mcp.CallToolRequest, a nodeArgs) (*mcp.CallToolResult, any, error) {
			l, e := store.Get()

			key := a.Label
			if key == "" {
				key = a.NodeID
			}

			ids := e.Find(key)
			if len(ids) == 0 {
				return text(fmt.Sprintf("No node matching %q.", key))
			}

			raw, _ := json.MarshalIndent(graph.NodeMap(l.G.Node(ids[0])), "", "  ")

			return text(string(raw) + fmt.Sprintf("\ndegree: %d", l.G.Degree(ids[0])))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_neighbors", Description: "Get all direct neighbors of a node with edge details."},
		func(_ context.Context, _ *mcp.CallToolRequest, a neighborArgs) (*mcp.CallToolResult, any, error) {
			l, e := store.Get()

			key := a.Label
			if key == "" {
				key = a.NodeID
			}

			ids := e.Find(key)
			if len(ids) == 0 {
				return text(fmt.Sprintf("No node matching %q.", key))
			}

			id := ids[0]

			var lines []string

			for _, nb := range l.G.Neighbors(id) {
				x := l.G.Edge(id, nb)
				if a.RelationFilter != "" && x.Relation != a.RelationFilter {
					continue
				}

				lines = append(lines, fmt.Sprintf("%s --%s [%s]--> %s  (%s %s)",
					l.G.Node(x.Source).Label, x.Relation, x.Confidence, l.G.Node(x.Target).Label, x.SourceFile, x.SourceLocation))
			}

			return text(truncate(fmt.Sprintf("Neighbors of %s (%d):\n%s", l.G.Node(id).Label, len(lines), strings.Join(lines, "\n")),
				budget(a.TokenBudget, 2000)))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_community", Description: "Get all nodes in a community by community ID."},
		func(_ context.Context, _ *mcp.CallToolRequest, a communityArgs) (*mcp.CallToolResult, any, error) {
			l, _ := store.Get()

			ms, ok := l.Communities[a.CommunityID]
			if !ok {
				return text(fmt.Sprintf("No community %d.", a.CommunityID))
			}

			sorted := append([]string(nil), ms...)
			sort.SliceStable(sorted, func(i, j int) bool { return l.G.Degree(sorted[i]) > l.G.Degree(sorted[j]) })

			lines := []string{fmt.Sprintf("Community %d %q (%d nodes):", a.CommunityID, l.Labels[a.CommunityID], len(ms))}
			for _, m := range sorted {
				n := l.G.Node(m)
				lines = append(lines, fmt.Sprintf("- %s [%s %s] degree=%d", n.Label, n.SourceFile, n.SourceLocation, l.G.Degree(m)))
			}

			return text(truncate(strings.Join(lines, "\n"), budget(a.TokenBudget, 2000)))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "god_nodes", Description: "Return the most connected nodes - the core abstractions of the knowledge graph."},
		func(_ context.Context, _ *mcp.CallToolRequest, a godArgs) (*mcp.CallToolResult, any, error) {
			l, _ := store.Get()

			var lines []string
			for i, g := range analyze.GodNodes(l.G, budget(a.TopN, 10), a.ExcludeHubsPercentile) {
				lines = append(lines, fmt.Sprintf("%d. %s - %d edges", i+1, g.Label, g.Degree))
			}

			return text(strings.Join(lines, "\n"))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "graph_stats", Description: "Return summary statistics: node count, edge count, communities, confidence breakdown."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
			l, _ := store.Get()

			return text(statsText(l))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "shortest_path", Description: "Find the shortest path between two concepts in the knowledge graph."},
		func(_ context.Context, _ *mcp.CallToolRequest, a pathArgs) (*mcp.CallToolResult, any, error) {
			_, e := store.Get()

			return text(e.Path(a.Source, a.Target))
		})

	addResources(s, store)

	return s
}

func statsText(l *graph.Loaded) string {
	conf := map[string]int{}
	rel := map[string]int{}

	for _, e := range l.G.Edges() {
		c := e.Confidence
		if c == "" {
			c = model.Extracted
		}

		conf[c]++
		rel[e.Relation]++
	}

	lines := []string{
		fmt.Sprintf("Nodes: %d", l.G.NumNodes()),
		fmt.Sprintf("Edges: %d", l.G.NumEdges()),
		fmt.Sprintf("Communities: %d", len(l.Communities)),
	}

	for _, k := range []string{model.Extracted, model.Inferred, model.Ambiguous} {
		lines = append(lines, fmt.Sprintf("%s: %d", k, conf[k]))
	}

	return strings.Join(lines, "\n")
}

func addResources(s *mcp.Server, store *Store) {
	res := func(uri, name, desc, mime string, fn func(*graph.Loaded) string) {
		s.AddResource(&mcp.Resource{URI: uri, Name: name, Description: desc, MIMEType: mime},
			func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				l, _ := store.Get()

				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: mime, Text: fn(l)}}}, nil
			})
	}

	res("graphify://stats", "Graph Stats", "Node/edge/community counts and confidence breakdown", "text/plain", statsText)
	res("graphify://god-nodes", "God Nodes", "Top 10 most-connected nodes", "text/plain", func(l *graph.Loaded) string {
		var lines []string
		for i, g := range analyze.GodNodes(l.G, 10, 0) {
			lines = append(lines, fmt.Sprintf("%d. %s - %d edges", i+1, g.Label, g.Degree))
		}

		return strings.Join(lines, "\n")
	})
	res("graphify://report", "Graph Report", "Full GRAPH_REPORT.md", "text/markdown", func(_ *graph.Loaded) string {
		raw, err := os.ReadFile(strings.TrimSuffix(store.path, "graph.json") + "GRAPH_REPORT.md")
		if err != nil {
			return "GRAPH_REPORT.md not found; run `bag extract` or `bag cluster-only`."
		}

		return string(raw)
	})
}

// HTTPOptions configure the HTTP server.
type HTTPOptions struct {
	Addr     string
	Path     string
	APIKey   string
	Version  string
	Stateles bool
}

// ServeHTTP serves MCP (streamable HTTP) plus a small REST API until ctx is
// cancelled.
func ServeHTTP(ctx context.Context, store *Store, opt HTTPOptions) error {
	srv := NewMCPServer(store, opt.Version)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: opt.Stateles})

	server := ada.New()
	server.Use(mrecover.Middleware(), mrequestid.Middleware(), mlog.Middleware())

	server.GET("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	auth := authMiddleware(opt.APIKey)

	path := opt.Path
	if path == "" {
		path = "/mcp"
	}

	server.Handle(path, handler, auth)
	server.HandleWildcard(path+"/", handler, auth)

	api := server.Group("/api/v1", auth)

	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}

	api.GET("/stats", func(w http.ResponseWriter, _ *http.Request) {
		l, _ := store.Get()
		writeJSON(w, map[string]any{"nodes": l.G.NumNodes(), "edges": l.G.NumEdges(), "communities": len(l.Communities)})
	})

	api.GET("/query", func(w http.ResponseWriter, r *http.Request) {
		_, e := store.Get()
		q := r.URL.Query()
		writeJSON(w, map[string]string{"result": e.Query(q.Get("q"), query.Options{DFS: q.Get("mode") == "dfs"})})
	})

	api.GET("/path", func(w http.ResponseWriter, r *http.Request) {
		_, e := store.Get()
		writeJSON(w, map[string]string{"result": e.Path(r.URL.Query().Get("from"), r.URL.Query().Get("to"))})
	})

	api.GET("/explain", func(w http.ResponseWriter, r *http.Request) {
		_, e := store.Get()
		writeJSON(w, map[string]string{"result": e.Explain(r.URL.Query().Get("q"))})
	})

	slog.Info("serving", "addr", opt.Addr, "mcp", path)

	return server.StartWithContext(ctx, opt.Addr)
}

func authMiddleware(key string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if key == "" {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := r.Header.Get("X-API-Key")
			if b, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
				got = b
			}

			if subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ServeStdio runs the MCP server over stdin/stdout.
func ServeStdio(ctx context.Context, store *Store, version string) error {
	return NewMCPServer(store, version).Run(ctx, &mcp.StdioTransport{})
}
