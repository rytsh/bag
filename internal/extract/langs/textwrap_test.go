package langs

import "testing"

// Expected values produced by Python's textwrap.shorten(width=..., placeholder="…").
func TestShortenLabel(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"aa bb-cc dd", 10, "aa bb-cc…"},
		{"xx a-b-c-d-e-f-g-h", 10, "xx a-b-c-…"},
		{"foo--bar baz", 10, "foo--bar…"},
		{"x 123-456 yy", 10, "x 123-456…"},
		{"hello world-wide-web is here ok", 10, "hello…"},
		{"Find a Cargo.toml under *root* when none sits directly at the root. A bounded-depth search", 80,
			"Find a Cargo.toml under *root* when none sits directly at the root. A bounded-…"},
		{"short", 80, "short"},
	}

	for _, c := range cases {
		if got := ShortenLabel(c.in, c.width); got != c.want {
			t.Errorf("ShortenLabel(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
		}
	}
}
