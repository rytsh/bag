package ids

import (
	"encoding/json"
	"os"
	"testing"
)

type golden struct {
	Normalize []struct {
		In   string `json:"in"`
		Norm string `json:"norm"`
	} `json:"normalize"`
	Make []struct {
		In []string `json:"in"`
		ID string   `json:"id"`
	} `json:"make"`
}

// testdata/golden.json is generated from Graphify's graphify.ids module.
func TestGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}

	var g golden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}

	for _, c := range g.Normalize {
		if got := NormalizeID(c.In); got != c.Norm {
			t.Errorf("NormalizeID(%q) = %q, want %q", c.In, got, c.Norm)
		}
	}

	for _, c := range g.Make {
		if got := MakeID(c.In...); got != c.ID {
			t.Errorf("MakeID(%q) = %q, want %q", c.In, got, c.ID)
		}
	}
}

func TestIdempotentAndCaseless(t *testing.T) {
	for _, s := range []string{"İslemYap", "Straße", "\u0345\u0301x", "ΣΊΣΥΦΟΣ", "Hello.World()"} {
		n := NormalizeID(s)
		if NormalizeID(n) != n {
			t.Errorf("not idempotent for %q", s)
		}
	}
}
