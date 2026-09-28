package graph

import "testing"

// Expected values come from graphify._minhash.MinHash(128) updated with
// b"abc" and b"bcd".
func TestMinHashMatchesGraphify(t *testing.T) {
	m := newMinHash("ab cd")

	want := []uint64{2134366058, 245323478, 291926927, 1428977732}
	for i, w := range want {
		if m[i] != w {
			t.Fatalf("hashvalues[%d] = %d, want %d", i, m[i], w)
		}
	}
}

func TestJaroMatchesRapidfuzz(t *testing.T) {
	// rapidfuzz halves transpositions with integer division:
	// Jaro.normalized_similarity("dcba abce", "abcd ecba") == 0.85185...
	if got := jaro("dcba abce", "abcd ecba"); got < 0.85185 || got > 0.85186 {
		t.Fatalf("jaro = %v", got)
	}
}
