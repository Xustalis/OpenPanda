// SPDX-License-Identifier: AGPL-3.0-or-later

// Command genicons renders the OpenPanda PWA icon set from a pure-stdlib
// drawing so the manifest is installable and push notifications carry the app
// mark without shipping a drawing library.
//
// The mark is the North Star & Orbit symbol: a four-point polar star inside an
// open orbit, drawn on the same 64×64 grid as webui/app/public/logo/*.svg.
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
	bg    = color.RGBA{0x05, 0x07, 0x0b, 0xff} // #05070b deep-space night
	orbit = color.RGBA{0x6f, 0xc7, 0xa1, 0xff} // #6fc7a1 jade orbit
	star  = color.RGBA{0xf2, 0xe8, 0xcf, 0xff} // #f2e8cf starlight cream
)

func main() {
	out := "webui/app/public/icons"
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatalf("mkdir %s: %v", out, err)
	}
	// Sizes follow the shipped set: favicons are optically larger on their
	// rounded tile; launcher icons keep the mark at the designer's ~64%.
	render(filepath.Join(out, "favicon-16.png"), 16, true, true, 0.95)
	render(filepath.Join(out, "favicon-32.png"), 32, true, true, 0.94)
	render(filepath.Join(out, "favicon-48.png"), 48, true, true, 0.86)
	render(filepath.Join(out, "apple-touch-icon-180.png"), 180, true, false, 0.74)
	render(filepath.Join(out, "icon-192.png"), 192, true, false, 0.64)
	render(filepath.Join(out, "icon-512.png"), 512, true, false, 0.64)
	render(filepath.Join(out, "icon-1024.png"), 1024, true, false, 0.64)
	render(filepath.Join(out, "avatar-400.png"), 400, true, false, 0.68)
	render(filepath.Join(out, "badge-72.png"), 72, false, false, 0.82)
	// The site ships its own copy of the touch icon next to index.html.
	src := filepath.Join(out, "apple-touch-icon-180.png")
	if data, err := os.ReadFile(src); err == nil {
		if err := os.WriteFile(filepath.Join("site", "apple-touch-icon.png"), data, 0o644); err != nil {
			log.Printf("site touch icon: %v", err)
		}
	}
}

// render draws the mark at size s. withBG fills the night background
// (installable icon), rounded for the favicon set; the notification badge is a
// transparent mark instead.
func render(path string, s int, withBG, rounded bool, scale float64) {
	const ss = 4 // 4× supersampling: stdlib has no rasteriser, so AA is ours
	n := s * ss
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	if withBG {
		if rounded {
			fillRoundedRect(img, 0, 0, float64(n), float64(n), 0.22*float64(n), bg)
		} else {
			draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)
		}
	}
	drawMark(img, float64(n), scale)
	out := downsample(img, s, ss)
	f, err := os.Create(path)
	if err != nil {
		log.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	if err := png.Encode(f, out); err != nil {
		log.Fatalf("encode %s: %v", path, err)
	}
}

// fillRoundedRect paints a rounded rectangle; the caller's image is at 4×, so
// a binary inside test is enough.
func fillRoundedRect(img *image.RGBA, x0, y0, x1, y1, r float64, c color.RGBA) {
	for y := int(y0); y < int(y1); y++ {
		for x := int(x0); x < int(x1); x++ {
			cx, cy := float64(x)+0.5, float64(y)+0.5
			inside := cx >= x0 && cx <= x1 && cy >= y0 && cy <= y1
			if inside && (cx < x0+r || cx > x1-r) && (cy < y0+r || cy > y1-r) {
				// In a corner block: inside only when within the corner circle.
				ccx, ccy := x0+r, y0+r
				if cx > x1-r {
					ccx = x1 - r
				}
				if cy > y1-r {
					ccy = y1 - r
				}
				inside = math.Hypot(cx-ccx, cy-ccy) <= r
			}
			if inside {
				blend(img, x, y, c, 1)
			}
		}
	}
}

// drawMark paints the symbol on a 64-unit grid, scaled to px pixels and
// centred. scale is the mark's optical share of the canvas.
func drawMark(img *image.RGBA, px, scale float64) {
	k := px / 64 * scale
	off := px/2 - 32*k // the grid's origin once (32,32) sits at the centre
	at := func(x, y float64) point { return point{off + x*k, off + y*k} }

	// The star first: the orbit crosses over it in the shipped artwork, and
	// drawing the ring after keeps the same overlap.
	fillPolygon(img, []point{
		at(32, 12), at(36.6, 27.4), at(52, 32), at(36.6, 36.6),
		at(32, 52), at(27.4, 36.6), at(12, 32), at(27.4, 27.4),
	}, star)

	drawOrbit(img, k, off)
	fillCircle(img, off+56.1*k, off+22.3*k, 2.9*k, orbit)
	fillCircle(img, off+39.2*k, off+20.3*k, 1.9*k, orbit)
}

// drawOrbit strokes the open ellipse. SVG's pathLength normalisation is
// 82% dash / 18% gap; here the same ratio is applied to the real perimeter,
// so the gap lands where the design puts it and round caps close the ends.
func drawOrbit(img *image.RGBA, k, off float64) {
	const samples = 8192
	rx, ry := 26*k, 9*k
	rot := -22 * math.Pi / 180
	cos, sin := math.Cos(rot), math.Sin(rot)
	cx, cy := off+32*k, off+32*k

	pts := make([]point, samples)
	lens := make([]float64, samples)
	total := 0.0
	var prev point
	for i := 0; i < samples; i++ {
		t := 2 * math.Pi * float64(i) / samples
		x, y := rx*math.Cos(t), ry*math.Sin(t)
		p := point{cx + x*cos - y*sin, cy + x*sin + y*cos}
		pts[i] = p
		if i > 0 {
			total += math.Hypot(p.x-prev.x, p.y-prev.y)
		}
		lens[i] = total
		prev = p
	}

	stroke := 2.3 * k
	radius := stroke / 2
	// Space the stamps finely enough that consecutive disks overlap and the
	// stroke reads as one line; denser than the stroke radius keeps caps round.
	spacing := math.Max(0.35, radius/6)
	step := int(math.Max(1, float64(samples)*spacing/total))
	for i := 0; i < samples; i += step {
		phase := math.Mod(lens[i]/total*100, 100)
		if phase >= 82 {
			continue // the open gap in the orbit
		}
		fillCircle(img, pts[i].x, pts[i].y, radius, orbit)
	}
}

type point struct{ x, y float64 }

// fillPolygon scan-fills a polygon whose points are already in pixels; the
// caller's image is at 4×, so a binary point-in-polygon test is enough.
func fillPolygon(img *image.RGBA, poly []point, c color.RGBA) {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, p := range poly {
		minX = math.Min(minX, p.x)
		maxX = math.Max(maxX, p.x)
		minY = math.Min(minY, p.y)
		maxY = math.Max(maxY, p.y)
	}
	for y := int(math.Floor(minY)); y <= int(math.Ceil(maxY)); y++ {
		for x := int(math.Floor(minX)); x <= int(math.Ceil(maxX)); x++ {
			if pointInPolygon(float64(x)+0.5, float64(y)+0.5, poly) {
				blend(img, x, y, c, 1)
			}
		}
	}
}

func pointInPolygon(x, y float64, poly []point) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		if (poly[i].y > y) != (poly[j].y > y) &&
			x < (poly[j].x-poly[i].x)*(y-poly[i].y)/(poly[j].y-poly[i].y)+poly[i].x {
			in = !in
		}
	}
	return in
}

// fillCircle blends an anti-aliased filled circle centered at (cx, cy) with
// radius r into img. A pixel's coverage is 1 inside (r-0.5) and ramps to 0 at
// (r+0.5), giving a one-pixel smooth edge once downsampled.
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
	if !image.Pt(x, y).In(img.Bounds()) {
		return
	}
	dst := img.RGBAAt(x, y)
	srcA := a * float64(c.A) / 255
	dstA := float64(dst.A) / 255
	outA := srcA + dstA*(1-srcA)
	if outA == 0 {
		img.SetRGBA(x, y, color.RGBA{})
		return
	}
	mix := func(s, d uint8) uint8 {
		v := (float64(s)*srcA + float64(d)*dstA*(1-srcA)) / outA
		return uint8(math.Round(math.Min(255, v)))
	}
	img.SetRGBA(x, y, color.RGBA{mix(c.R, dst.R), mix(c.G, dst.G), mix(c.B, dst.B), uint8(math.Round(outA * 255))})
}

// downsample averages ss×ss blocks, preserving the alpha-weighted colour.
func downsample(img *image.RGBA, size, ss int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a float64
			for dy := 0; dy < ss; dy++ {
				for dx := 0; dx < ss; dx++ {
					p := img.RGBAAt(x*ss+dx, y*ss+dy)
					w := float64(p.A) / 255
					r += float64(p.R) * w
					g += float64(p.G) * w
					b += float64(p.B) * w
					a += w
				}
			}
			if a == 0 {
				out.SetRGBA(x, y, color.RGBA{})
				continue
			}
			n := float64(ss * ss)
			out.SetRGBA(x, y, color.RGBA{
				uint8(math.Round(r / a)),
				uint8(math.Round(g / a)),
				uint8(math.Round(b / a)),
				uint8(math.Round(a / n * 255)),
			})
		}
	}
	return out
}
