// Unit tests for the perceptual dedup primitives: dHash on synthetic
// images (solid, gradients, PNG round-trip) + the offset-tolerant
// sliding-window matcher. No real video — that's the ffmpeg benchmark.
package video

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"testing"
)

// gradient builds a WxH grayscale image whose columns increase (or
// decrease) left→right — a deterministic dHash fixture.
func gradient(increasing bool) *image.Gray {
	const w, h = 180, 80
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := x * 255 / (w - 1)
			if !increasing {
				v = 255 - v
			}
			img.SetGray(x, y, color.Gray{Y: uint8(v)})
		}
	}
	return img
}

func solid(v uint8) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, 40, 40))
	for i := range img.Pix {
		img.Pix[i] = v
	}
	return img
}

func TestDHash_SolidIsZero(t *testing.T) {
	// Every cell equal → no left<right → all bits 0.
	if h := DHash(solid(128)); h != 0 {
		t.Errorf("solid image dHash = %016x, want 0", h)
	}
}

func TestDHash_Gradients(t *testing.T) {
	// Strictly-increasing columns → every left<right → all 64 bits set.
	if h := DHash(gradient(true)); h != ^uint64(0) {
		t.Errorf("increasing gradient dHash = %016x, want all-ones", h)
	}
	// Strictly-decreasing columns → no left<right → 0.
	if h := DHash(gradient(false)); h != 0 {
		t.Errorf("decreasing gradient dHash = %016x, want 0", h)
	}
}

func TestDHash_Deterministic(t *testing.T) {
	g := gradient(true)
	first := DHash(g)
	second := DHash(g)
	if first != second {
		t.Error("dHash is not deterministic")
	}
}

func TestDHashPNG_RoundTrip(t *testing.T) {
	g := gradient(true)
	var buf bytes.Buffer
	if err := png.Encode(&buf, g); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	got, err := DHashPNG(buf.Bytes())
	if err != nil {
		t.Fatalf("DHashPNG: %v", err)
	}
	if got != DHash(g) {
		t.Errorf("DHashPNG %016x != DHash %016x (lossless round-trip should match)", got, DHash(g))
	}
	if _, err := DHashPNG([]byte("not-a-png")); err == nil {
		t.Error("DHashPNG should error on non-PNG input")
	}
}

func TestFrameHashVersionIncludesPreprocessingAndInterval(t *testing.T) {
	a := CurrentFrameHashVersion(0.1)
	b := CurrentFrameHashVersion(0.10)
	c := CurrentFrameHashVersion(0.25)
	if a != b {
		t.Fatalf("equivalent intervals produced %q and %q", a, b)
	}
	if a == c {
		t.Fatalf("different intervals produced the same version %q", a)
	}
	if !CompatibleFrameHashVersions("", LegacyFrameHashVersion) {
		t.Fatal("omitted pre-FF-041 version should normalize to legacy")
	}
	if CompatibleFrameHashVersions(a, LegacyFrameHashVersion) {
		t.Fatal("bounded and legacy preprocessing must never compare")
	}
}

func TestHamming(t *testing.T) {
	cases := []struct {
		a, b uint64
		want int
	}{
		{0, 0, 0},
		{0, 0xFF, 8},
		{0, ^uint64(0), 64},
		{0b1010, 0b0101, 4},
	}
	for _, c := range cases {
		if got := Hamming(c.a, c.b); got != c.want {
			t.Errorf("Hamming(%x,%x) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestMatch_Identical(t *testing.T) {
	a := []uint64{1, 2, 4, 8, 16}
	if !Match(a, a, 0, 3, 0) {
		t.Error("identical sequences should match")
	}
}

func TestMatch_Offset(t *testing.T) {
	a := []uint64{1, 2, 4, 8, 16}
	b := []uint64{99, 99, 1, 2, 4, 8, 16} // a shifted 2 frames into b
	if !Match(a, b, 0, 3, 0) {
		t.Error("offset copy should match at the aligned offset")
	}
	evidence := BestAlignment(a, b, 0, 0)
	if evidence != (AlignmentEvidence{LeftStart: 0, RightStart: 2, Frames: 5}) {
		t.Fatalf("alignment = %+v, want left=0 right=2 frames=5 gaps=0", evidence)
	}
	reversed := BestAlignment(b, a, 0, 0)
	if reversed != (AlignmentEvidence{LeftStart: 2, RightStart: 0, Frames: 5}) {
		t.Fatalf("reversed alignment = %+v, want swapped offsets", reversed)
	}
}

func TestMatch_NoMatch(t *testing.T) {
	a := []uint64{1, 2, 4}
	b := []uint64{^uint64(1), ^uint64(2), ^uint64(4)} // ~64 bits off each
	if Match(a, b, 10, 3, 0) {
		t.Error("bit-inverted sequence should not match within hamming 10")
	}
}

func TestMatch_TooShort(t *testing.T) {
	if Match([]uint64{1, 2}, []uint64{1, 2}, 0, 3, 0) {
		t.Error("sequences shorter than minRun can't match")
	}
}

func TestMatch_HammingThreshold(t *testing.T) {
	a := []uint64{0, 0, 0}
	b := []uint64{0x1F, 0x1F, 0x1F} // 5 bits set → hamming 5 vs 0
	if !Match(a, b, 10, 3, 0) {
		t.Error("5-bit diff within maxHamming=10 should match")
	}
	if Match(a, b, 4, 3, 0) {
		t.Error("5-bit diff over maxHamming=4 should not match")
	}
}

func TestMatch_GapTolerance(t *testing.T) {
	a := []uint64{1, 2, 4, 8, 16}
	b := []uint64{1, 2, ^uint64(0), 8, 16} // frame 2 is far off — one bad frame
	if Match(a, b, 0, 5, 0) {
		t.Error("strict window (0 gaps) should not reach a 5-run across the miss")
	}
	if !Match(a, b, 0, 5, 1) {
		t.Error("one tolerated gap should bridge into a 5-run")
	}
	if evidence := BestAlignment(a, b, 0, 1); evidence != (AlignmentEvidence{Frames: 5, Gaps: 1}) {
		t.Fatalf("gap-tolerant alignment = %+v, want full five-frame span with one gap", evidence)
	}
}

func TestBestAlignment_EmptyAndNegativeGapBudget(t *testing.T) {
	if evidence := BestAlignment(nil, []uint64{1}, 0, 0); evidence != (AlignmentEvidence{}) {
		t.Fatalf("empty alignment = %+v, want zero value", evidence)
	}
	a := []uint64{0, 0, 0}
	b := []uint64{0, ^uint64(0), 0}
	if evidence := BestAlignment(a, b, 0, -1); evidence.Frames != 1 || evidence.Gaps != 0 {
		t.Fatalf("negative gap budget alignment = %+v, want strict one-frame run", evidence)
	}
}

// TestBestAlignmentPreservesBooleanMatcherPolicy compares the evidence-bearing
// implementation with the former window-only algorithm over deterministic
// random sequences. The production match gate must not drift as diagnostics
// become richer.
func TestBestAlignmentPreservesBooleanMatcherPolicy(t *testing.T) {
	rng := rand.New(rand.NewSource(81)) //nolint:gosec // deterministic test fixture
	for trial := 0; trial < 1_000; trial++ {
		left := make([]uint64, 1+rng.Intn(80))
		right := make([]uint64, 1+rng.Intn(80))
		for i := range left {
			left[i] = rng.Uint64()
		}
		for i := range right {
			right[i] = rng.Uint64()
		}
		maxHamming := rng.Intn(65)
		maxGaps := rng.Intn(6)
		minRun := 1 + rng.Intn(50)
		want := referenceMatch(left, right, maxHamming, minRun, maxGaps)
		if got := Match(left, right, maxHamming, minRun, maxGaps); got != want {
			t.Fatalf("trial %d match=%t want=%t lengths=%d/%d hamming=%d run=%d gaps=%d",
				trial, got, want, len(left), len(right), maxHamming, minRun, maxGaps)
		}
	}
}

// referenceMatch preserves the pre-evidence matcher shape inside the test.
func referenceMatch(a, b []uint64, maxHamming, minRun, maxGaps int) bool {
	if len(a) < minRun || len(b) < minRun {
		return false
	}
	best := 0
	for offset := -(len(a) - 1); offset <= len(b)-1; offset++ {
		var aligned []bool
		for i := range a {
			j := i + offset
			if j >= 0 && j < len(b) {
				aligned = append(aligned, Hamming(a[i], b[j]) <= maxHamming)
			}
		}
		left, misses := 0, 0
		for right, matched := range aligned {
			if !matched {
				misses++
			}
			for misses > maxGaps {
				if !aligned[left] {
					misses--
				}
				left++
			}
			if width := right - left + 1; width > best {
				best = width
			}
		}
	}
	return best >= minRun
}
