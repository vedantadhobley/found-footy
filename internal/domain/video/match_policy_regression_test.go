// Production-derived tiered-dedup regression boundaries.
package video

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type partialOverlapFixture struct {
	LeftHashesHex  string `json:"left_hashes_hex"`
	RightHashesHex string `json:"right_hashes_hex"`
}

// TestMatch_RaphinhaPartialOverlapBoundary preserves the manually classified
// Raphinha 37′ failure: a direct goal clip and a longer tactical-analysis edit
// share broadcast frames, but are distinct whole-video compositions. The
// selected 12/30/3 and 16/50/5 routes must reject the pair while the next
// reviewed unsafe boundaries continue to prove the fixture is meaningful.
func TestMatch_RaphinhaPartialOverlapBoundary(t *testing.T) {
	raw, err := os.ReadFile("testdata/raphinha-partial-overlap.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture partialOverlapFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	left := decodeHashFixture(t, fixture.LeftHashesHex)
	right := decodeHashFixture(t, fixture.RightHashesHex)
	if len(left) != 50 || len(right) != 50 {
		t.Fatalf("fixture lengths = %d/%d, want 50/50", len(left), len(right))
	}

	if Match(left, right, 12, 30, 3) {
		t.Fatal("primary 12/30/3 route collapsed distinct compositions")
	}
	if Match(left, right, 16, 50, 5) {
		t.Fatal("sustained 16/50/5 route collapsed distinct compositions")
	}
	if !Match(left, right, 14, 30, 3) {
		t.Fatal("fixture no longer reproduces the unsafe primary Hamming-14 boundary")
	}
	if !Match(left, right, 18, 50, 5) {
		t.Fatal("fixture no longer reproduces the unsafe sustained Hamming-18 boundary")
	}
}

func decodeHashFixture(t *testing.T, encoded string) []uint64 {
	t.Helper()
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw)%8 != 0 {
		t.Fatalf("fixture has %d bytes, want a multiple of 8", len(raw))
	}
	hashes := make([]uint64, len(raw)/8)
	for i := range hashes {
		hashes[i] = binary.BigEndian.Uint64(raw[i*8:])
	}
	return hashes
}
