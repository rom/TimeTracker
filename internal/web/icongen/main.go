// Command icongen draws the application's icon at every size it is served in.
//
// The icons are generated rather than drawn by hand, and generated here rather
// than by a design tool, for the same reason the PDF writer is written in Go
// (docs/adr/0007-pure-go-document-generation.md): the build must not depend on
// tooling nobody has. A checked-in generator means the icons can be reproduced
// exactly, and a change to the mark is a change to one shape rather than to
// eight files somebody has to remember to re-export.
//
// The mark is the same clock as static/icons/favicon.svg: a ring and two hands.
// The geometry below is that file's, divided by its 32-unit viewBox, so the two
// cannot drift apart without somebody editing both.
//
// Run it with:
//
//	go generate ./internal/web/...
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// The mark, in fractions of the icon's width. Taken from favicon.svg's 32-unit
// viewBox: a circle at (16,16) with radius 13 and a 3-wide stroke, and a hand
// path from (16,8) through (16,17) to (22,21).
const (
	markCentre = 16.0 / 32
	markRadius = 13.0 / 32
	markStroke = 3.0 / 32

	handTopY     = 8.0 / 32
	handPivotY   = 17.0 / 32
	handTipX     = 22.0 / 32
	handTipY     = 21.0 / 32
	handStroke   = 3.0 / 32
	cornerRadius = 6.0 / 32
)

// The palette. These are the light theme's entity blue and red, which are also
// the two halves of the logotype - so the icon on a home screen is recognisably
// the same thing as the name in the header.
var (
	ring       = color.NRGBA{R: 0x25, G: 0x63, B: 0xeb, A: 0xff}
	hands      = color.NRGBA{R: 0xc0, G: 0x26, B: 0x26, A: 0xff}
	background = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
)

// samples is the supersampling factor per axis.
//
// Sixteen samples per pixel. The shapes here are circles and thick segments,
// whose edges are the entire visual quality of a 16-pixel favicon, and the whole
// job takes milliseconds at any size worth generating.
const samples = 4

func main() {
	dir := flag.String("dir", "internal/web/static/icons", "where to write the icons")
	flag.Parse()

	if err := run(*dir); err != nil {
		fmt.Fprintln(os.Stderr, "icongen:", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// shape describes how much of the icon the mark occupies and what sits
	// behind it.
	type icon struct {
		name string
		size int
		// scale shrinks the mark within the canvas. A maskable icon is cropped
		// to whatever silhouette the platform likes - a circle, a squircle, a
		// teardrop - and only the middle 80% is guaranteed to survive, so its
		// mark is drawn smaller and its background runs to the edge.
		scale     float64
		rounded   bool
		fullBleed bool
	}

	icons := []icon{
		// Tab icons. Small enough that the rounded corners would be mush, so
		// they are transparent and the mark stands alone.
		{name: "favicon-16.png", size: 16, scale: 1},
		{name: "favicon-32.png", size: 32, scale: 1},
		{name: "favicon-48.png", size: 48, scale: 1},

		// iOS home screen. Opaque, because iOS composites an icon onto black
		// and a transparent one arrives looking like a hole. iOS rounds the
		// corners itself, so this does not.
		{name: "apple-touch-icon.png", size: 180, scale: 0.72, fullBleed: true},

		// Android and desktop installs, shown as drawn.
		{name: "icon-192.png", size: 192, scale: 0.78, rounded: true},
		{name: "icon-512.png", size: 512, scale: 0.78, rounded: true},

		// Adaptive icons, cropped to the platform's silhouette.
		{name: "icon-maskable-192.png", size: 192, scale: 0.56, fullBleed: true},
		{name: "icon-maskable-512.png", size: 512, scale: 0.56, fullBleed: true},
	}

	for _, spec := range icons {
		img := draw(spec.size, spec.scale, spec.rounded, spec.fullBleed)
		if err := writePNG(filepath.Join(dir, spec.name), img); err != nil {
			return err
		}
	}

	// A .ico as well. Browsers request /favicon.ico whether or not a page links
	// to one, and a 404 on every page load is noise in the log for no reason.
	if err := writeICO(filepath.Join(dir, "favicon.ico"),
		draw(16, 1, false, false),
		draw(32, 1, false, false),
		draw(48, 1, false, false),
	); err != nil {
		return err
	}

	fmt.Printf("icongen: wrote %d icons to %s\n", len(icons)+1, dir)
	return nil
}

// opticalSizeLimit is the size below which the mark is adjusted for pixels.
//
// A shape that is right at 512 is not right at 16. The SVG's 3-unit stroke is
// 1.5 pixels there, which straddles two pixel rows and renders every edge as
// half-grey - so the ring and the hands blur into a blob rather than reading as
// a clock. Below this size the stroke is thinned, snapped to whole pixels, and
// the ring opened up slightly to leave room inside.
//
// This is ordinary optical sizing and the reason hand-tuned small icons exist at
// all; it is done here so it happens by rule rather than by somebody remembering
// to redraw the 16.
const opticalSizeLimit = 64

// Small sizes use a slightly wider, thinner ring than the SVG's exact geometry.
const (
	smallRadius = 14.0 / 32
	smallStroke = 2.5 / 32
)

// draw renders the mark at one size.
//
// Coverage is computed by supersampling rather than by a vector library: the
// whole mark is a ring and two round-capped segments, and "how far is this point
// from that circle" answers both exactly. It is less code than a path
// rasteriser, has no dependency, and there is nothing here whose edge quality it
// could not reach.
func draw(size int, scale float64, rounded, fullBleed bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	dim := float64(size)

	// The mark's geometry in pixels, scaled about the centre.
	at := func(unit float64) float64 { return (unit-0.5)*scale*dim + dim/2 }
	length := func(unit float64) float64 { return unit * scale * dim }

	radiusUnit, ringStrokeUnit, handStrokeUnit := markRadius, markStroke, handStroke
	if size < opticalSizeLimit {
		radiusUnit, ringStrokeUnit, handStrokeUnit = smallRadius, smallStroke, smallStroke
	}

	var (
		cx, cy   = dim / 2, dim / 2
		radius   = length(radiusUnit)
		ringHalf = length(ringStrokeUnit) / 2
		handHalf = length(handStrokeUnit) / 2

		topX, topY     = at(markCentre), at(handTopY)
		pivotX, pivotY = at(markCentre), at(handPivotY)
		tipX, tipY     = at(handTipX), at(handTipY)

		corner = cornerRadius * dim
	)

	if size < opticalSizeLimit {
		// Whole-pixel strokes on a half-pixel grid, so an edge lands on a pixel
		// boundary instead of across one. A 1-pixel line drawn at 50% coverage
		// twice is what makes a small icon look smudged.
		ringHalf = math.Max(1, math.Round(length(ringStrokeUnit))) / 2
		handHalf = math.Max(1, math.Round(length(handStrokeUnit))) / 2
		radius = math.Round(radius*2) / 2
	}

	step := 1.0 / float64(samples)
	offset := step / 2

	for y := range size {
		for x := range size {
			var bgCover, ringCover, handCover float64

			for sy := range samples {
				for sx := range samples {
					px := float64(x) + float64(sx)*step + offset
					py := float64(y) + float64(sy)*step + offset

					switch {
					case fullBleed:
						bgCover++
					case rounded:
						if insideRoundedRect(px, py, dim, corner) {
							bgCover++
						}
					}

					// The ring: within half a stroke of the circle itself.
					if math.Abs(math.Hypot(px-cx, py-cy)-radius) <= ringHalf {
						ringCover++
					}

					// The hands, as two round-capped segments. Round caps come
					// free from a distance test against the segment.
					if distanceToSegment(px, py, topX, topY, pivotX, pivotY) <= handHalf ||
						distanceToSegment(px, py, pivotX, pivotY, tipX, tipY) <= handHalf {
						handCover++
					}
				}
			}

			total := float64(samples * samples)
			pixel := color.NRGBA{}
			pixel = over(pixel, background, bgCover/total)
			pixel = over(pixel, ring, ringCover/total)
			// The hands last: they cross the ring and should sit on top of it,
			// exactly as the SVG's source order puts them.
			pixel = over(pixel, hands, handCover/total)
			img.SetNRGBA(x, y, pixel)
		}
	}
	return img
}

// over composites a colour onto another at a given coverage, in
// straight (non-premultiplied) alpha.
func over(dst, src color.NRGBA, coverage float64) color.NRGBA {
	if coverage <= 0 {
		return dst
	}
	if coverage > 1 {
		coverage = 1
	}
	srcAlpha := coverage * float64(src.A) / 255
	dstAlpha := float64(dst.A) / 255
	outAlpha := srcAlpha + dstAlpha*(1-srcAlpha)
	if outAlpha == 0 {
		return color.NRGBA{}
	}
	channel := func(s, d uint8) uint8 {
		value := (float64(s)*srcAlpha + float64(d)*dstAlpha*(1-srcAlpha)) / outAlpha
		return uint8(math.Round(clamp(value, 0, 255)))
	}
	return color.NRGBA{
		R: channel(src.R, dst.R),
		G: channel(src.G, dst.G),
		B: channel(src.B, dst.B),
		A: uint8(math.Round(outAlpha * 255)),
	}
}

func clamp(value, low, high float64) float64 {
	return math.Min(math.Max(value, low), high)
}

// distanceToSegment returns the distance from a point to a line segment.
func distanceToSegment(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	lengthSquared := dx*dx + dy*dy
	if lengthSquared == 0 {
		return math.Hypot(px-ax, py-ay)
	}
	t := clamp(((px-ax)*dx+(py-ay)*dy)/lengthSquared, 0, 1)
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

// insideRoundedRect reports whether a point is within a rounded square filling
// the canvas.
func insideRoundedRect(px, py, dim, radius float64) bool {
	// Distance from the nearest corner's centre, which is what rounds it.
	x := math.Max(math.Abs(px-dim/2)-(dim/2-radius), 0)
	y := math.Max(math.Abs(py-dim/2)-(dim/2-radius), 0)
	return math.Hypot(x, y) <= radius
}

// writePNG encodes an image to a file.
func writePNG(path string, img image.Image) error {
	var buf bytes.Buffer
	// Best compression: these are written once and served for the life of a
	// release, so the bytes are worth more than the milliseconds.
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&buf, img); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// writeICO packs images into a Windows icon file.
//
// An .ico is a small header, one directory entry per image, and the images
// themselves - which since Windows Vista may be PNG rather than the old DIB
// format. That is the whole format for this purpose, so it is written here
// rather than pulled in.
func writeICO(path string, images ...image.Image) error {
	var payloads [][]byte
	for _, img := range images {
		var buf bytes.Buffer
		encoder := png.Encoder{CompressionLevel: png.BestCompression}
		if err := encoder.Encode(&buf, img); err != nil {
			return err
		}
		payloads = append(payloads, buf.Bytes())
	}

	var out bytes.Buffer
	// ICONDIR: reserved, type 1 (icon), image count.
	_ = binary.Write(&out, binary.LittleEndian, uint16(0))
	_ = binary.Write(&out, binary.LittleEndian, uint16(1))
	_ = binary.Write(&out, binary.LittleEndian, uint16(len(images)))

	// The images follow the directory, so the first one starts after all of it.
	const dirEntrySize = 16
	offset := 6 + dirEntrySize*len(images)

	for i, img := range images {
		bounds := img.Bounds()
		// A dimension of 256 is encoded as zero, which is why the field is a
		// byte. Nothing here is that large, but encoding it correctly costs a
		// line and being wrong would be invisible until somebody added one.
		dimension := func(n int) uint8 {
			if n >= 256 {
				return 0
			}
			return uint8(n)
		}
		out.WriteByte(dimension(bounds.Dx()))
		out.WriteByte(dimension(bounds.Dy()))
		out.WriteByte(0)                                        // palette size: none, it is a PNG
		out.WriteByte(0)                                        // reserved
		_ = binary.Write(&out, binary.LittleEndian, uint16(1))  // colour planes
		_ = binary.Write(&out, binary.LittleEndian, uint16(32)) // bits per pixel
		_ = binary.Write(&out, binary.LittleEndian, uint32(len(payloads[i])))
		_ = binary.Write(&out, binary.LittleEndian, uint32(offset))
		offset += len(payloads[i])
	}

	for _, payload := range payloads {
		out.Write(payload)
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}
