// Command genicons renders the PANDA PWA icon set from a pure-stdlib drawing so
// the manifest is installable and push notifications carry an app mark without
// shipping binary assets or a third-party drawing library (design P2-17).
//
// Run via `make icons`, or directly: go run ./scripts/genicons
package main

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

var (
	bg    = color.RGBA{0x2f, 0x9e, 0x6b, 0xff} // #2f9e6b bamboo green (matches favicon.svg)
	white = color.RGBA{0xfa, 0xf6, 0xee, 0xff} // cream panda face
	black = color.RGBA{0x1d, 0x2b, 0x24, 0xff} // soft charcoal patches
	blush = color.RGBA{0xf2, 0xa7, 0xb3, 0xff} // rosy cheeks
)

func main() {
	out := "webui/app/public/icons"
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatalf("mkdir %s: %v", out, err)
	}
	render(filepath.Join(out, "icon-192.png"), 192, true)
	render(filepath.Join(out, "icon-512.png"), 512, true)
	render(filepath.Join(out, "badge-72.png"), 72, false)
}

// render draws a panda face at size s. withBG fills the dark app background
// (installable icon); the badge is a transparent monochrome mark instead.
func render(path string, s int, withBG bool) {
	img := image.NewRGBA(image.Rect(0, 0, s, s))
	if withBG {
		draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)
	}
	drawPanda(img, s)
	f, err := os.Create(path)
	if err != nil {
		log.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		log.Fatalf("encode %s: %v", path, err)
	}
}

// drawPanda paints the cheerful geometric panda face: two round ears, a cream
// face, glossy tilted eye patches, rosy blush cheeks, and a happy open smile —
// mirroring webui/app/src/brand/panda.tsx. Coordinates are fractions of s so
// the mark scales across icon sizes.
func drawPanda(img *image.RGBA, s int) {
	f := float64(s)
	// ears (behind the face)
	fillCircle(img, 0.21*f, 0.26*f, 0.15*f, black)
	fillCircle(img, 0.79*f, 0.26*f, 0.15*f, black)
	// face
	fillCircle(img, 0.50*f, 0.56*f, 0.36*f, white)
	// eye patches (slightly bigger, tilted inward)
	fillCircle(img, 0.35*f, 0.49*f, 0.11*f, black)
	fillCircle(img, 0.65*f, 0.49*f, 0.11*f, black)
	// glossy pupils with sparkle
	fillCircle(img, 0.369*f, 0.509*f, 0.039*f, white)
	fillCircle(img, 0.631*f, 0.509*f, 0.039*f, white)
	fillCircle(img, 0.383*f, 0.496*f, 0.014*f, black)
	fillCircle(img, 0.645*f, 0.496*f, 0.014*f, black)
	// blush cheeks
	fillCircle(img, 0.242*f, 0.633*f, 0.053*f, blush)
	fillCircle(img, 0.758*f, 0.633*f, 0.053*f, blush)
	// nose
	fillCircle(img, 0.50*f, 0.641*f, 0.05*f, black)
	// happy smile — a stamped arc under the nose
	smileArc(img, 0.50*f, 0.641*f, 0.117*f, 0.034*f, black, f)
}

// smileArc stamps small filled circles along a downward semicircle centered at
// (cx, cy) with radius r — the poor man's stroke for a bezier-free smile.
func smileArc(img *image.RGBA, cx, cy, r, dot float64, c color.RGBA, f float64) {
	steps := 14
	for i := 0; i <= steps; i++ {
		theta := math.Pi * float64(i) / float64(steps) // 0..π = lower half
		x := cx + r*math.Cos(theta)
		y := cy + r*math.Sin(theta)*0.78 // slightly flattened smile
		fillCircle(img, x, y, dot*(0.9+0.1*math.Sin(theta)), c)
	}
}

// fillCircle blends an anti-aliased filled circle centered at (cx, cy) with
// radius r into img. A pixel's coverage is 1 inside (r-0.5) and ramps to 0 at
// (r+0.5), giving a one-pixel smooth edge without external dependencies.
func fillCircle(img *image.RGBA, cx, cy, r float64, c color.RGBA) {
	x0 := int(math.Floor(cx - r - 1))
	x1 := int(math.Ceil(cx + r + 1))
	y0 := int(math.Floor(cy - r - 1))
	y1 := int(math.Ceil(cy + r + 1))
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			a := r + 0.5 - d
			if a <= 0 {
				continue
			}
			if a > 1 {
				a = 1
			}
			blend(img, x, y, c, a)
		}
	}
}

// blend composites c with coverage a over the pixel at (x, y).
func blend(img *image.RGBA, x, y int, c color.RGBA, a float64) {
	b := img.Bounds()
	if x < b.Min.X || x >= b.Max.X || y < b.Min.Y || y >= b.Max.Y {
		return
	}
	dst := img.RGBAAt(x, y)
	na := float64(dst.A)/255 + a*(1-float64(dst.A)/255)
	img.SetRGBA(x, y, color.RGBA{
		R: uint8(math.Round(float64(c.R)*a + float64(dst.R)*(1-a))),
		G: uint8(math.Round(float64(c.G)*a + float64(dst.G)*(1-a))),
		B: uint8(math.Round(float64(c.B)*a + float64(dst.B)*(1-a))),
		A: uint8(math.Round(na * 255)),
	})
}
