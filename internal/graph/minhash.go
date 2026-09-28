package graph

import (
	"crypto/sha1" //nolint:gosec // matches Graphify's hash family
	"encoding/binary"
	"sort"
	"strings"
)

// MinHash + band LSH, bit-compatible with Graphify's graphify/_minhash.py
// (Apache-2.0): same Mersenne-prime permutation family, the same numpy
// RandomState(1) coefficients and the same (bands, rows) = (14, 9) layout for
// threshold 0.7 / 128 permutations, so the fuzzy pass sees the same
// candidate pairs.

const (
	minhashPerm  = 128
	lshBands     = 14
	lshRows      = 9
	mersenne61   = (1 << 61) - 1
	minhashMask  = 0xFFFF_FFFF
	shingleWidth = 3
)

type minHash [minhashPerm]uint64

// newMinHash builds the signature of text's 3-shingles (spaces stripped).
//
// Adapted from Graphify's dedup._make_minhash (Apache-2.0).
func newMinHash(text string) *minHash {
	var m minHash
	for i := range m {
		m[i] = minhashMask
	}

	for sh := range shingles(strings.ReplaceAll(text, " ", "")) {
		sum := sha1.Sum([]byte(sh)) //nolint:gosec // compatibility, not security
		hv := uint64(binary.LittleEndian.Uint32(sum[:4]))

		for i := range m {
			// numpy uint64 arithmetic wraps exactly like Go's.
			p := ((minhashA[i]*hv + minhashB[i]) % mersenne61) & minhashMask
			if p < m[i] {
				m[i] = p
			}
		}
	}

	return &m
}

func shingles(text string) map[string]struct{} {
	r := []rune(text)
	if len(r) < shingleWidth {
		return map[string]struct{}{text: {}}
	}

	out := make(map[string]struct{}, len(r))
	for i := 0; i+shingleWidth <= len(r); i++ {
		out[string(r[i:i+shingleWidth])] = struct{}{}
	}

	return out
}

type bandKey [lshRows]uint64

// lsh is a band-hashing index over MinHash signatures.
type lsh struct {
	tables [lshBands]map[bandKey][]int
}

func newLSH() *lsh {
	l := &lsh{}
	for i := range l.tables {
		l.tables[i] = map[bandKey][]int{}
	}

	return l
}

func band(m *minHash, i int) bandKey {
	var k bandKey

	copy(k[:], m[i*lshRows:(i+1)*lshRows])

	return k
}

func (l *lsh) insert(key int, m *minHash) {
	for i := range l.tables {
		b := band(m, i)
		l.tables[i][b] = append(l.tables[i][b], key)
	}
}

// query returns the keys sharing at least one band with m, ascending.
func (l *lsh) query(m *minHash) []int {
	seen := map[int]bool{}

	var out []int

	for i := range l.tables {
		for _, k := range l.tables[i][band(m, i)] {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}

	sort.Ints(out)

	return out
}

// Coefficients of numpy.random.RandomState(1).randint over [1, 2^61-1) and [0, 2^61-1).
var minhashA = [128]uint64{
	0x0ac1f425ff4780ec, 0x18672f8ceebc1449, 0x00077eff20ccc38a, 0x0d65aacbffc11e86,
	0x0591cb4f3c7053c1, 0x17a3809065865082, 0x0faebfcc634e1e48, 0x1876aaedab7479fd,
	0x05928d86ef7f7d1a, 0x09efe4b2d8a7d515, 0x0b5054fe5032b166, 0x0f6a8b928648c9d5,
	0x1456fb8b718620fd, 0x00cc4dea3ac5929d, 0x0702df9d88cf598f, 0x0ba3c232e9f96a45,
	0x0ad47cd7750b5fd8, 0x0f064be96e4242f2, 0x03f065f7f06aaddf, 0x12b6c760c7448457,
	0x0cfd988db749d7ea, 0x17dffd89cd818408, 0x103c913f17c1cb3e, 0x113c0e1684a5a53a,
	0x005b0a01dd71f781, 0x0504f13cd442f8d2, 0x15c57508d460e2d9, 0x09ff7e8d45e69a74,
	0x0b7a01af0f2a8feb, 0x00cdf279aba7b9c9, 0x192d421e97d32448, 0x0bcdb583abf185c7,
	0x15383f95696aee32, 0x087d85393292b204, 0x111edbc44a252b19, 0x10c5a1f12461fc2c,
	0x0fbe864cc8874c1b, 0x15aa0734699c2551, 0x04ae8a6d08bf7374, 0x000975299fc06dd3,
	0x1d26000fa91f6c41, 0x1f87c8c44c6a301a, 0x07cb2d6f7235dee3, 0x0a0e35d738dd2788,
	0x1a6d051a12c7fe9a, 0x12a9266878200417, 0x0899b709189ebec4, 0x0b2a4be7e743447f,
	0x09aba5171e96ed7e, 0x01498d648659409c, 0x04f53ba51568513a, 0x0dc82a53eab76ca7,
	0x162d4288e9132521, 0x03fadda24c86af0b, 0x1dd7bd17959a8690, 0x0da92aef90df9c58,
	0x12f95f199d2b0fc8, 0x059000f3f4df855d, 0x16dcba4a42cf84bf, 0x13235d2e3b23d3a1,
	0x1a3296d888901498, 0x09ff92b7f32f2542, 0x11c435717e39274e, 0x0a07a7038a64cb81,
	0x0cc9bff8c3f6d4fe, 0x093081cd0b9bc707, 0x09ee723423d4d1d6, 0x03cfc5c6cadaf603,
	0x11d0f64c07a10fdc, 0x16287895e21482cc, 0x074559078a71184e, 0x03318ac872aed44c,
	0x03a79d4ce463042c, 0x0eb1321460a95e1f, 0x05ce262489d63368, 0x0a54a707a6fd0f2e,
	0x0d7133c45c7b9a3a, 0x19072ef0922d9dfd, 0x003538d2a3494061, 0x19db010d20562c0b,
	0x02205917b0b13f7d, 0x1fa8fb51a5d2e788, 0x003dc3795a9bc099, 0x195166cac3633ddd,
	0x0519fe945b45a9a1, 0x0558cc8cc0b6bcc2, 0x0d97625ee19f9e3d, 0x16ffc1e902fcc099,
	0x09d74d527f841374, 0x1f27736112e40883, 0x1d5ffdecc975a6dd, 0x1311ba671066b763,
	0x132d710a5af59eb7, 0x14115760f11c39ea, 0x087ba752613ac9d7, 0x1cadccc6c34ebac3,
	0x0743e147c5873fe8, 0x12dca8b04d25f637, 0x00bc1c0fc5d23b86, 0x1dfd3591272668ab,
	0x139f001494215a77, 0x06ed47b0024e5917, 0x02cd19e5b583cf8e, 0x1b7607f1787bea73,
	0x0895c261c3bc56b6, 0x1f94875477a70c0b, 0x040cdc6044dd40b8, 0x0def993dd4e9ce39,
	0x10de9ed98d23a196, 0x1f508ce711f0dc61, 0x0c1e81f978f41fd4, 0x031b5419be25d5f2,
	0x0ebe938e3124088e, 0x1262acd476dd1e75, 0x10e5632b3af90e87, 0x016606cd821c82b9,
	0x00fe07bb3564bb90, 0x0c4b56180c9f33fc, 0x16267c8984d421c3, 0x1fd038c72c1a6f36,
	0x0516dd456574c9a5, 0x06b5c3651b7c4af9, 0x073f18158282ed29, 0x1f07b04d1a2588dc,
	0x1c2acab149e96972, 0x09f0d52f3b5485ce, 0x0d85bda8f7b915cf, 0x178f562d473fe758,
}

var minhashB = [128]uint64{
	0x1fc9d2903bceaf9c, 0x077894ea17703e2d, 0x15f451c391efe374, 0x18376d426afd3bce,
	0x0f9fad2e5e2af980, 0x04c6119dd020703f, 0x0cf6434b4a2db623, 0x1ba433b5b7a6f9dd,
	0x0e9e72219cea2a82, 0x034ad5d46d49f653, 0x1d111430c07abf36, 0x1f435d206d8520fd,
	0x0e65c29c6daa7837, 0x02eebad25ca5c51f, 0x0f56c41c274130de, 0x1f105d4aefebb488,
	0x0b67c06dea99d663, 0x1b84bc20c82e6e08, 0x19c816549dc7bbcd, 0x168672320937424f,
	0x0f499ca9a781c340, 0x0336de6c213894d3, 0x126ce8184b5b2471, 0x179f6bea5c5e5b14,
	0x116f082c464f250f, 0x140b069e12efa1db, 0x00986b8e26f275f3, 0x1e6b5f13295fd4fb,
	0x1207aa9af04f68eb, 0x14d3f0565f02a187, 0x18533ae30ccdfc35, 0x18c9c5afbdaf436e,
	0x1d66a9bc4c764ef3, 0x1e67b1223450d064, 0x1eb56ae4fb546720, 0x14381793e49d09c3,
	0x08237b18c264aed3, 0x04c168e5a6afd65e, 0x11ecf8a60a28b2af, 0x1c811ce7d05a3385,
	0x1b38684fc337fe3f, 0x11a03f71748d6457, 0x113ea9a08825c72a, 0x1d15f2ca1bfd72c2,
	0x146a2ed8256266e2, 0x0150ee9e823ffa91, 0x0d0af1443734db40, 0x1ece3cbcea9c0df4,
	0x0b52d8ce764fa091, 0x03d1c4a721dc44a3, 0x10fb51d1c3a5569c, 0x1ebdd6963675b726,
	0x01352aa9130299ca, 0x15cdf6cd186d6546, 0x00b4969929864ce6, 0x110888f05b413430,
	0x02a402b2186617be, 0x0e04652c24ace269, 0x1184e8b6ee56dd00, 0x03b3b556939b05f7,
	0x0650cb10d6f2fd13, 0x0cc037099f901270, 0x03676adc53139433, 0x10e2d36fba5ef88a,
	0x1c2d487b85d21144, 0x05ad7597bc9fad0e, 0x08669ebf2a580e15, 0x0e95b8aeafe30d7c,
	0x039254836d4379b8, 0x1bff2758ba82bcd0, 0x1de52faec19f3236, 0x12f2bbcf65c9da47,
	0x133ebdfcecd9ea0e, 0x0e80d2cd3419408f, 0x0a652d99020c7735, 0x043daed4ed2551f9,
	0x03d78bba4b651bd5, 0x1c69e2722abd56ee, 0x1ab4179d062b862c, 0x0caf2c2573b66096,
	0x0d1a39dbceef49b6, 0x0b321d4c5e4df20c, 0x051691789bf4fd3b, 0x16985f9a08ebc7cc,
	0x0c885bc75ac592d7, 0x005721a71419e27d, 0x02948c2bb17495cc, 0x0173137203412326,
	0x0582a65b75a4c270, 0x12ae2cfdf618e345, 0x13bdd6e2558d29a1, 0x03d848ab78da759a,
	0x0d1b32b81afaebc5, 0x15021e4980c99534, 0x01890259e2bc921b, 0x0f3ac82b88ccf401,
	0x1840a952480edc7f, 0x10aae25e5ac60fe0, 0x14fee19ae5729f54, 0x00fb221e3dd23b67,
	0x18408440061d4496, 0x07c7ca62f739df34, 0x15bab6836e126046, 0x0bdd628c58a65d27,
	0x0c7148e693bab453, 0x0924deef206404b0, 0x1d81413df33609c6, 0x195f4b0d5048cca4,
	0x03f9fd17f3ecbd96, 0x17ee65c237e89e7a, 0x0f0569dc3faad0f9, 0x0eaf2deadd221d5f,
	0x10b3e2353c3a8b89, 0x04945aa9d0aa65b9, 0x13ee73588b3cd96d, 0x0f1b4af630f66edd,
	0x09b5baf496be36cc, 0x1cae13520c52b16a, 0x0dccff8b04104069, 0x0b91d0240cb9dc70,
	0x0dd774906640149e, 0x0b297c7393affa6a, 0x001a58d4de159fb9, 0x1a0ce323c941eda9,
	0x0067915141343302, 0x1949e0d5150ea85d, 0x1aceaca42862b26d, 0x142f3d2e35a15350,
}
