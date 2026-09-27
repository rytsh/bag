package langs

import (
	"encoding/xml"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// Deterministic package-manifest ingestion: one canonical package node per
// package (keyed by name) plus depends_on edges.
//
// Adapted from Graphify's graphify/manifest_ingest.py (Apache-2.0).

type manifestInfo struct {
	name, version string
	deps          []string
}

var (
	goModModule   = regexp.MustCompile(`^module\s+(\S+)`)
	goModReqBlock = regexp.MustCompile(`^require\s*\(`)
	goModReqLine  = regexp.MustCompile(`^(\S+)\s+v\S+`)
	goModReqOne   = regexp.MustCompile(`^require\s+(\S+)\s+v\S+`)
	pep508Split   = regexp.MustCompile(`[\s<>=!~;\[\(]`)
	pomProp       = regexp.MustCompile(`\$\{([^}]+)\}`)
	pomXMLNS      = regexp.MustCompile(`\sxmlns="[^"]*"`)
	apmName       = regexp.MustCompile(`^name:\s*["']?([^"'\s#]+)`)
	apmVersion    = regexp.MustCompile(`^version:\s*["']?([^"'\s#]+)`)
	apmDepsHead   = regexp.MustCompile(`^dependencies:\s*$`)
	apmDepItem    = regexp.MustCompile(`^\s*-\s*["']?([^"'\s#:]+)`)
	apmDepKey     = regexp.MustCompile(`^\s{2,}([A-Za-z0-9._/@-]+)\s*:`)
)

func parseGoMod(text string) *manifestInfo {
	var (
		name  string
		deps  []string
		block bool
	)

	for _, line := range strings.Split(text, "\n") {
		s := strings.TrimSpace(line)

		if name == "" {
			if m := goModModule.FindStringSubmatch(s); m != nil {
				name = m[1]

				continue
			}
		}

		if goModReqBlock.MatchString(s) {
			block = true

			continue
		}

		if block {
			if strings.HasPrefix(s, ")") {
				block = false

				continue
			}

			if m := goModReqLine.FindStringSubmatch(s); m != nil {
				deps = append(deps, m[1])
			}
		} else if m := goModReqOne.FindStringSubmatch(s); m != nil {
			deps = append(deps, m[1])
		}
	}

	if name == "" {
		return nil
	}

	return &manifestInfo{name: name, deps: deps}
}

func coerceDeps(v any) []string {
	var out []string

	switch t := v.(type) {
	case map[string]any:
		for k := range t {
			out = append(out, k)
		}
	case []any:
		for _, it := range t {
			switch x := it.(type) {
			case string:
				out = append(out, x)
			case map[string]any:
				for k := range x {
					out = append(out, k)

					break
				}
			}
		}
	}

	return out
}

func strOf(v any) string {
	s, _ := v.(string)

	return s
}

func parsePyProject(text string) *manifestInfo {
	var data map[string]any
	if _, err := toml.Decode(text, &data); err != nil {
		return nil
	}

	proj, _ := data["project"].(map[string]any)
	tool, _ := data["tool"].(map[string]any)
	poetry, _ := tool["poetry"].(map[string]any)

	name := strOf(proj["name"])
	if name == "" {
		name = strOf(poetry["name"])
	}

	if name == "" {
		return nil
	}

	var deps []string

	if arr, ok := proj["dependencies"].([]any); ok {
		for _, s := range arr {
			if ss, ok := s.(string); ok {
				deps = append(deps, pep508Split.Split(strings.TrimSpace(ss), 2)[0])
			}
		}
	}

	if pd, ok := poetry["dependencies"].(map[string]any); ok {
		for k := range pd {
			if strings.ToLower(k) != "python" {
				deps = append(deps, k)
			}
		}
	}

	version := strOf(proj["version"])
	if version == "" {
		version = strOf(poetry["version"])
	}

	return &manifestInfo{name: name, version: version, deps: deps}
}

func parseCargo(text string) *manifestInfo {
	var data map[string]any
	if _, err := toml.Decode(text, &data); err != nil {
		return nil
	}

	pkg, _ := data["package"].(map[string]any)

	name := strOf(pkg["name"])
	if name == "" {
		return nil
	}

	deps := coerceDeps(data["dependencies"])

	if tg, ok := data["target"].(map[string]any); ok {
		for _, cfg := range tg {
			if m, ok := cfg.(map[string]any); ok {
				deps = append(deps, coerceDeps(m["dependencies"])...)
			}
		}
	}

	return &manifestInfo{name: name, version: strOf(pkg["version"]), deps: deps}
}

type pomDep struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
}

type pomDoc struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
	Parent     struct {
		GroupID string `xml:"groupId"`
		Version string `xml:"version"`
	} `xml:"parent"`
	Properties struct {
		Items []struct {
			XMLName xml.Name
			Value   string `xml:",chardata"`
		} `xml:",any"`
	} `xml:"properties"`
	Deps           []pomDep `xml:"dependencies>dependency"`
	DepMgmt        []pomDep `xml:"dependencyManagement>dependencies>dependency"`
	ProfileDeps    []pomDep `xml:"profiles>profile>dependencies>dependency"`
	BuildPluginDep []pomDep `xml:"build>plugins>plugin>dependencies>dependency"`
}

func parsePom(text string) *manifestInfo {
	text = pomXMLNS.ReplaceAllString(text, "")

	var d pomDoc
	if err := xml.Unmarshal([]byte(text), &d); err != nil || d.ArtifactID == "" {
		return nil
	}

	props := map[string]string{}
	for _, p := range d.Properties.Items {
		if v := strings.TrimSpace(p.Value); v != "" {
			props[p.XMLName.Local] = v
		}
	}

	gid := d.GroupID
	if gid == "" {
		gid = d.Parent.GroupID
	}

	version := d.Version
	if version == "" {
		version = d.Parent.Version
	}

	for k, v := range map[string]string{
		"project.groupId": gid, "project.artifactId": d.ArtifactID, "project.version": version,
		"project.parent.groupId": d.Parent.GroupID, "project.parent.version": d.Parent.Version,
	} {
		if _, ok := props[k]; !ok && v != "" {
			props[k] = strings.TrimSpace(v)
		}
	}

	res := func(v string) string {
		return pomProp.ReplaceAllStringFunc(strings.TrimSpace(v), func(m string) string {
			if r, ok := props[m[2:len(m)-1]]; ok {
				return r
			}

			return m
		})
	}

	gid, version = res(gid), res(version)

	name := d.ArtifactID
	if gid != "" {
		name = gid + ":" + d.ArtifactID
	}

	var deps []string

	for _, group := range [][]pomDep{d.Deps, d.DepMgmt, d.ProfileDeps, d.BuildPluginDep} {
		for _, dep := range group {
			a, g := res(dep.ArtifactID), res(dep.GroupID)
			if a == "" {
				continue
			}

			if g != "" {
				deps = append(deps, g+":"+a)
			} else {
				deps = append(deps, a)
			}
		}
	}

	return &manifestInfo{name: name, version: version, deps: deps}
}

func parseAPM(text string) *manifestInfo {
	var (
		name, version string
		deps          []string
		in            bool
	)

	for _, line := range strings.Split(text, "\n") {
		if !in {
			if m := apmName.FindStringSubmatch(line); m != nil {
				name = m[1]

				continue
			}

			if m := apmVersion.FindStringSubmatch(line); m != nil {
				version = m[1]

				continue
			}
		}

		if apmDepsHead.MatchString(line) {
			in = true

			continue
		}

		if in {
			if m := apmDepItem.FindStringSubmatch(line); m != nil {
				deps = append(deps, m[1])
			} else if m := apmDepKey.FindStringSubmatch(line); m != nil {
				deps = append(deps, m[1])
			} else if line != "" && line[0] != ' ' && line[0] != '\t' {
				in = false
			}
		}
	}

	if name == "" {
		return nil
	}

	return &manifestInfo{name: name, version: version, deps: deps}
}

var manifestParsers = map[string]struct {
	eco   string
	parse func(string) *manifestInfo
}{
	"apm.yml":        {"apm", parseAPM},
	"apm.yaml":       {"apm", parseAPM},
	"pyproject.toml": {"python", parsePyProject},
	"cargo.toml":     {"cargo", parseCargo},
	"go.mod":         {"go", parseGoMod},
	"pom.xml":        {"maven", parsePom},
}

// ExtractManifest extracts a package manifest.
func ExtractManifest(path, _ string, src []byte) *model.Extraction {
	if len(src) > 2_000_000 {
		return &model.Extraction{Error: "manifest too large to index"}
	}

	p, ok := manifestParsers[strings.ToLower(baseName(path))]
	if !ok {
		return &model.Extraction{Skipped: true}
	}

	info := p.parse(string(src))
	if info == nil || info.name == "" {
		return &model.Extraction{Skipped: true}
	}

	pid := ids.MakeID("pkg", info.name)
	n := &model.Node{
		ID: pid, Label: info.name, FileType: model.FileTypeCode, Type: "package",
		SourceFile: path, SourceLocation: "L1", Extra: map[string]any{"ecosystem": p.eco},
	}

	if info.version != "" {
		n.Extra["version"] = info.version
	}

	ex := &model.Extraction{Nodes: []*model.Node{n}}
	seen := map[string]bool{}

	for _, d := range info.deps {
		did := ids.MakeID("pkg", d)
		if d == "" || did == pid || seen[did] {
			continue
		}

		seen[did] = true
		ex.Edges = append(ex.Edges, &model.Edge{
			Source: pid, Target: did, Relation: "depends_on", Context: "dependency",
			Confidence: model.Extracted, ConfidenceScore: model.Score(1.0),
			SourceFile: path, SourceLocation: "L1", Weight: 1,
		})
	}

	return ex
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}

	return p
}

func registerManifests() {
	var names []string
	for k := range manifestParsers {
		names = append(names, k)
	}

	// Filenames are case-sensitive in the registry; register common casings.
	names = append(names, "Cargo.toml")

	extract.Register(&extract.Language{Name: "manifest", Filenames: names, Extract: ExtractManifest})
}
