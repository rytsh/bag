package langs

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// MCP server config extraction (.mcp.json, claude_desktop_config.json, ...):
// servers, their commands, packages and env var NAMES (values are never read).
//
// Adapted from Graphify's mcp_ingest.py (Apache-2.0).

var (
	mcpNpmPkgRe      = regexp.MustCompile(`^@[a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._-]*(?:@[\w.\-+]+)?$`)
	mcpPyPkgRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*-mcp(?:-[a-z0-9._-]+)?$|^mcp-[a-z0-9][a-z0-9._-]*$`)
	mcpArgFlagRe     = regexp.MustCompile(`^-{1,2}\w`)
	mcpControlChars  = regexp.MustCompile(`[\x00-\x1f\x7f]`)
	mcpMaxServers    = 200
	mcpMaxLabelRunes = 256
)

func mcpLabel(s string) string {
	s = mcpControlChars.ReplaceAllString(s, "")
	if r := []rune(s); len(r) > mcpMaxLabelRunes {
		s = string(r[:mcpMaxLabelRunes])
	}

	return s
}

// jsonPairs returns an object's (key, value) pairs in source order.
func jsonPairs(obj *tsx.Node) [][2]*tsx.Node {
	var out [][2]*tsx.Node

	if obj == nil || obj.Type() != "object" {
		return nil
	}

	for _, p := range obj.Children() {
		if p.Type() == "pair" {
			out = append(out, [2]*tsx.Node{p.Field("key"), p.Field("value")})
		}
	}

	return out
}

func jsonStringValue(n *tsx.Node) (string, bool) {
	if n == nil || n.Type() != "string" {
		return "", false
	}

	var s string
	if err := json.Unmarshal([]byte(n.Text()), &s); err != nil {
		return strings.Trim(n.Text(), `"`), true
	}

	return s, true
}

func jsonLookup(obj *tsx.Node, key string) *tsx.Node {
	var hit *tsx.Node

	for _, kv := range jsonPairs(obj) {
		if k, ok := jsonStringValue(kv[0]); ok && k == key {
			hit = kv[1] // last duplicate wins, like json.loads
		}
	}

	return hit
}

func mcpPackage(args *tsx.Node) string {
	if args == nil || args.Type() != "array" {
		return ""
	}

	for _, a := range args.NamedChildren() {
		s, ok := jsonStringValue(a)
		if !ok {
			continue
		}

		s = strings.TrimSpace(s)
		if s == "" || mcpArgFlagRe.MatchString(s) {
			continue
		}

		if mcpNpmPkgRe.MatchString(s) {
			if strings.HasPrefix(s, "@") {
				if i := strings.Index(s[1:], "@"); i >= 0 {
					return s[:i+1]
				}

				return s
			}

			if i := strings.Index(s, "@"); i >= 0 {
				return s[:i]
			}

			return s
		}

		if mcpPyPkgRe.MatchString(s) {
			return s
		}
	}

	return ""
}

// ExtractMCPConfig extracts an MCP server configuration file.
func ExtractMCPConfig(path, _ string, src []byte) *model.Extraction {
	if len(src) > jsonMaxBytes {
		return &model.Extraction{Error: "mcp config too large to index"}
	}

	if !json.Valid(src) {
		return &model.Extraction{Error: "mcp_ingest json error"}
	}

	tree, err := tsx.Parse("json", src)
	if err != nil {
		return &model.Extraction{Error: err.Error()}
	}
	defer tree.Release()

	doc := tree.Root
	if doc.Type() == "document" && len(doc.NamedChildren()) > 0 {
		doc = doc.NamedChildren()[0]
	}

	if doc.Type() != "object" {
		return &model.Extraction{Error: "mcp_ingest: root is not an object"}
	}

	servers := jsonLookup(doc, "mcpServers")
	if servers == nil || servers.Type() != "object" {
		servers = nil
		if nested := jsonLookup(doc, "mcp"); nested != nil && nested.Type() == "object" {
			servers = jsonLookup(nested, "servers")
		}

		if servers == nil || servers.Type() != "object" {
			return &model.Extraction{Error: "mcp_ingest: no mcpServers map"}
		}
	}

	b := base.NewBuilder(path)
	seenEdges := map[[3]string]bool{}

	addNode := func(id, label, kind string) {
		if id == "" || b.Has(id) {
			return
		}

		n := b.AddNode(id, mcpLabel(label), 1)
		n.Metadata = map[string]any{"mcp_kind": kind}
	}

	addEdge := func(src, tgt, rel, ctx string) {
		k := [3]string{src, tgt, rel}
		if src == "" || tgt == "" || src == tgt || seenEdges[k] {
			return
		}

		seenEdges[k] = true
		e := b.AddEdgeCtx(src, tgt, rel, 1, ctx)
		e.ConfidenceScore = model.Score(1.0)
	}

	addNode(b.FileID, filepath.Base(path), "mcp_config_file")

	// json.loads keeps the last duplicate key at the first key's position.
	var (
		order []string
		specs = map[string]*tsx.Node{}
	)

	for _, kv := range jsonPairs(servers) {
		name, ok := jsonStringValue(kv[0])
		if !ok {
			continue
		}

		if _, seen := specs[name]; !seen {
			order = append(order, name)
		}

		specs[name] = kv[1]
	}

	count := 0

	for _, name := range order {
		spec := specs[name]
		if name == "" || spec == nil || spec.Type() != "object" {
			continue
		}

		if count >= mcpMaxServers {
			break
		}

		count++

		sid := ids.MakeID(b.Stem, "mcp_server", name)
		addNode(sid, name, "mcp_server")
		addEdge(b.FileID, sid, "contains", "")

		if cmd, ok := jsonStringValue(jsonLookup(spec, "command")); ok && strings.TrimSpace(cmd) != "" {
			cmd = strings.TrimSpace(cmd)
			cid := ids.MakeID("mcp_command", cmd)
			addNode(cid, cmd, "mcp_command")
			addEdge(sid, cid, "references", "command")
		}

		if pkg := mcpPackage(jsonLookup(spec, "args")); pkg != "" {
			pid := ids.MakeID("mcp_package", pkg)
			addNode(pid, pkg, "mcp_package")
			addEdge(sid, pid, "references", "package")
		}

		if env := jsonLookup(spec, "env"); env != nil && env.Type() == "object" {
			for _, kv := range jsonPairs(env) {
				k, ok := jsonStringValue(kv[0])
				if !ok || k == "" {
					continue
				}

				eid := ids.MakeID("env_var", k)
				addNode(eid, k, "env_var")
				addEdge(sid, eid, "requires_env", "")
			}
		}
	}

	return b.ResultUnfiltered()
}
