// hash.go — perceptual (dHash) frame hashing for video dedup. Pure Go:
// grayscale (ITU-R 601) → histogram equalize → 9×8 area-downscale →
// difference hash (64 bits). Ported from archive/src/activities/hashing.py.
// We reproduce the ALGORITHM (grayscale → equalize → downscale → diff), not
// PIL's exact pixel arithmetic: the rebuild starts on a fresh corpus, so
// internal consistency + threshold validation on real clusters are what
// matter, not bit-for-bit Python parity (decisions.md, rung 2). See
// match.go for the sliding-window comparison.
package video

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
)

// dHash samples a 9-wide × 8-tall grid: 8 horizontal comparisons per row ×
// 8 rows = 64 bits.
const (
	dhashW = 9
	dhashH = 8
)

// DHashPNG decodes a PNG-encoded frame (as the ffmpeg adapter emits) and
// returns its dHash.
func DHashPNG(data []byte) (uint64, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, fmt.Errorf("video.DHashPNG: decode: %w", err)
	}
	return DHash(img), nil
}

// DHash computes the difference hash of img: full-res grayscale → in-place
// histogram equalization → area-average downscale to 9×8 → compare each
// cell to its right neighbor (1 bit each, MSB-first).
func DHash(img image.Image) uint64 {
	g := toGray(img)
	equalize(g)
	cells := downscale(g, dhashW, dhashH)

	var hash uint64
	bit := 0
	for row := 0; row < dhashH; row++ {
		for col := 0; col < dhashW-1; col++ {
			if cells[row*dhashW+col] < cells[row*dhashW+col+1] {
				hash |= 1 << uint(63-bit)
			}
			bit++
		}
	}
	return hash
}

// grayImg is a minimal 8-bit grayscale buffer (avoids repeated interface
// dispatch through image.Image during equalize/downscale).
type grayImg struct {
	pix  []uint8
	w, h int
}

// toGray converts img to 8-bit grayscale via ITU-R 601 luma weights —
// the same conversion PIL's Image.convert("L") uses.
func toGray(img image.Image) *grayImg {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	g := &grayImg{pix: make([]uint8, w*h), w: w, h: h}
	i := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, gg, bb, _ := img.At(x, y).RGBA() // 16-bit per channel
			l := (299*(r>>8) + 587*(gg>>8) + 114*(bb>>8)) / 1000
			// The weighted average of three 8-bit channels is always in [0,255].
			g.pix[i] = uint8(l) //nolint:gosec // Proven by the luma bounds above.
			i++
		}
	}
	return g
}

// equalize applies standard CDF-based histogram equalization in place —
// normalizes brightness/contrast so different encodes of the same footage
// hash alike. A flat image is left untouched.
func equalize(g *grayImg) {
	var hist [256]int
	for _, p := range g.pix {
		hist[p]++
	}
	var cdf [256]int
	sum := 0
	for i := 0; i < 256; i++ {
		sum += hist[i]
		cdf[i] = sum
	}
	total := len(g.pix)
	cdfMin := 0
	for i := 0; i < 256; i++ {
		if cdf[i] != 0 {
			cdfMin = cdf[i]
			break
		}
	}
	denom := total - cdfMin
	if denom <= 0 {
		return // flat / single-value image — nothing to spread
	}
	var lut [256]uint8
	for i := 0; i < 256; i++ {
		v := (cdf[i] - cdfMin) * 255 / denom
		if v < 0 {
			v = 0
		}
		lut[i] = uint8(v)
	}
	for i, p := range g.pix {
		g.pix[i] = lut[p]
	}
}

// downscale area-averages g into a tw×th grid (row-major). Handles a source
// smaller than the target (each output cell covers ≥1 source pixel).
func downscale(g *grayImg, tw, th int) []uint8 {
	out := make([]uint8, tw*th)
	for ty := 0; ty < th; ty++ {
		y0 := ty * g.h / th
		y1 := (ty + 1) * g.h / th
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for tx := 0; tx < tw; tx++ {
			x0 := tx * g.w / tw
			x1 := (tx + 1) * g.w / tw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			sum, n := 0, 0
			for yy := y0; yy < y1 && yy < g.h; yy++ {
				for xx := x0; xx < x1 && xx < g.w; xx++ {
					sum += int(g.pix[yy*g.w+xx])
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			// sum/n is an average of uint8 pixels and therefore stays in [0,255].
			out[ty*tw+tx] = uint8(sum / n) //nolint:gosec // Proven by the pixel bounds.
		}
	}
	return out
}
