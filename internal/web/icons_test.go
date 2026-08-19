package web

import (
	"bytes"
	"encoding/json"
	"image"
	_ "image/png"
	"io/fs"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The icons are the one part of the interface a user sees before the
// application runs - in a tab, on a home screen, in an install prompt - and
// every way of getting them wrong is invisible until somebody installs the
// thing. Hence tests for the platform rules rather than for the drawing.

// TestEveryLinkedIconExists walks the icons the page actually asks for.
//
// A <link> pointing at a file that is not embedded is a broken icon in every
// tab, and nothing else in the suite would notice: the page still renders.
func TestEveryLinkedIconExists(t *testing.T) {
	srv, _ := newTestServer(t)
	body := get(t, srv, "/today").Body.String()

	references := regexp.MustCompile(`(?:href|content|src)="(/static/icons/[^"]+)"`).
		FindAllStringSubmatch(body, -1)
	if len(references) < 5 {
		t.Fatalf("the page links %d icons; it should link the whole set", len(references))
	}

	for _, reference := range references {
		path := reference[1]
		t.Run(path, func(t *testing.T) {
			rec := get(t, srv, path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", path, rec.Code)
			}
			if rec.Body.Len() == 0 {
				t.Fatal("the file is empty")
			}
		})
	}
}

// TestLinkedIconSizesAreTheDeclaredOnes.
//
// A sizes attribute is a promise a browser believes: it will pick the 32x32 for
// a tab without opening it. Declaring one size and shipping another means the
// browser scales the wrong file.
func TestLinkedIconSizesAreTheDeclaredOnes(t *testing.T) {
	srv, _ := newTestServer(t)
	body := get(t, srv, "/today").Body.String()

	pattern := regexp.MustCompile(`<link[^>]*href="(/static/icons/[^"]+\.png)"[^>]*sizes="(\d+)x(\d+)"`)
	alternate := regexp.MustCompile(`<link[^>]*sizes="(\d+)x(\d+)"[^>]*href="(/static/icons/[^"]+\.png)"`)

	type claim struct {
		path          string
		width, height string
	}
	var claims []claim
	for _, m := range pattern.FindAllStringSubmatch(body, -1) {
		claims = append(claims, claim{m[1], m[2], m[3]})
	}
	for _, m := range alternate.FindAllStringSubmatch(body, -1) {
		claims = append(claims, claim{m[3], m[1], m[2]})
	}
	if len(claims) == 0 {
		t.Fatal("no icon declares a size; the browser has to open every one to choose")
	}

	for _, c := range claims {
		config := decodeConfig(t, srv, c.path)
		if strconv.Itoa(config.Width) != c.width || strconv.Itoa(config.Height) != c.height {
			t.Errorf("%s declares %sx%s and is %dx%d",
				c.path, c.width, c.height, config.Width, config.Height)
		}
	}
}

// TestAppleTouchIconIsOpaque.
//
// iOS composites a home-screen icon onto black. A transparent one arrives
// looking like a hole punched in the screen, which is the classic way to ship a
// broken home-screen icon and never find out.
func TestAppleTouchIconIsOpaque(t *testing.T) {
	for _, name := range []string{
		"static/icons/apple-touch-icon.png",
		"static/icons/icon-maskable-192.png",
		"static/icons/icon-maskable-512.png",
	} {
		t.Run(name, func(t *testing.T) {
			img := decodeImage(t, name)
			bounds := img.Bounds()
			for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
				for x := bounds.Min.X; x < bounds.Max.X; x++ {
					if _, _, _, alpha := img.At(x, y).RGBA(); alpha != 0xffff {
						t.Fatalf("pixel (%d,%d) is not opaque; iOS composites this onto black",
							x, y)
					}
				}
			}
		})
	}
}

// TestMaskableIconsKeepTheirMarkInTheSafeZone.
//
// A maskable icon is cropped to whatever silhouette the platform prefers - a
// circle, a squircle, a teardrop - and only the middle 80% survives all of
// them. Anything outside that radius will be clipped on some device nobody
// testing here owns.
func TestMaskableIconsKeepTheirMarkInTheSafeZone(t *testing.T) {
	for _, name := range []string{
		"static/icons/icon-maskable-192.png",
		"static/icons/icon-maskable-512.png",
	} {
		t.Run(name, func(t *testing.T) {
			img := decodeImage(t, name)
			bounds := img.Bounds()
			size := float64(bounds.Dx())
			centre := size / 2
			// 80% of the width is the guaranteed area, so its radius is 40%.
			safeRadius := size * 0.4

			// The mark is whatever is not the background, and the background is
			// whatever the corner is - which the opacity test above has already
			// established is a real colour rather than transparency.
			background := img.At(bounds.Min.X, bounds.Min.Y)
			bgR, bgG, bgB, _ := background.RGBA()

			var worst float64
			for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
				for x := bounds.Min.X; x < bounds.Max.X; x++ {
					r, g, b, _ := img.At(x, y).RGBA()
					// A generous threshold: anti-aliasing against the
					// background produces near-background pixels that are not
					// part of the mark in any meaningful sense.
					if absDiff(r, bgR)+absDiff(g, bgG)+absDiff(b, bgB) < 0x3000 {
						continue
					}
					dx, dy := float64(x)+0.5-centre, float64(y)+0.5-centre
					if distance := math.Hypot(dx, dy); distance > worst {
						worst = distance
					}
				}
			}

			if worst == 0 {
				t.Fatal("no mark was found at all; the icon is a plain square")
			}
			if worst > safeRadius {
				t.Errorf("the mark reaches %.1f pixels from the centre, past the %.1f "+
					"safe radius; a circular mask would clip it", worst, safeRadius)
			}
		})
	}
}

// TestManifestIsInstallable checks the fields a browser requires before it will
// offer to install, and that every icon it names actually exists.
func TestManifestIsInstallable(t *testing.T) {
	raw, err := staticFS.ReadFile("static/icons/site.webmanifest")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	var manifest struct {
		Name            string `json:"name"`
		ShortName       string `json:"short_name"`
		StartURL        string `json:"start_url"`
		Display         string `json:"display"`
		ThemeColor      string `json:"theme_color"`
		BackgroundColor string `json:"background_color"`
		Icons           []struct {
			Src     string `json:"src"`
			Sizes   string `json:"sizes"`
			Type    string `json:"type"`
			Purpose string `json:"purpose"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("the manifest is not valid JSON: %v", err)
	}

	if manifest.Name == "" || manifest.ShortName == "" {
		t.Error("a manifest without a name is not installable")
	}
	if manifest.StartURL == "" {
		t.Error("a manifest without a start_url launches nowhere")
	}
	if manifest.Display != "standalone" {
		t.Errorf("display = %q; a home-screen launch should not open in a tab", manifest.Display)
	}
	if manifest.ThemeColor == "" || manifest.BackgroundColor == "" {
		t.Error("without both colours an installed launch flashes white before the page paints")
	}

	// Chrome will not offer to install without a 192 and a 512, and Android
	// will not draw an adaptive icon without a maskable one.
	var has192, has512, hasMaskable bool
	for _, icon := range manifest.Icons {
		if _, err := fs.Stat(staticFS, "static"+strings.TrimPrefix(icon.Src, "/static")); err != nil {
			t.Errorf("the manifest names %s, which is not embedded", icon.Src)
			continue
		}
		if strings.Contains(icon.Purpose, "maskable") {
			hasMaskable = true
		}
		switch icon.Sizes {
		case "192x192":
			has192 = true
		case "512x512":
			has512 = true
		}
	}
	if !has192 || !has512 {
		t.Error("an installable manifest needs both a 192x192 and a 512x512 icon")
	}
	if !hasMaskable {
		t.Error("without a maskable icon Android draws the square one inside its own shape")
	}
}

// TestRootIconPathsAreServed: a browser asks for /favicon.ico whether or not the
// page links to one, and several tools probe the apple-touch paths the same way.
func TestRootIconPathsAreServed(t *testing.T) {
	srv, _ := newTestServer(t)

	for _, path := range []string{
		"/favicon.ico",
		"/apple-touch-icon.png",
		"/apple-touch-icon-precomposed.png",
		"/site.webmanifest",
	} {
		rec := get(t, srv, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
			continue
		}
		if rec.Body.Len() == 0 {
			t.Errorf("GET %s served nothing", path)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s: X-Content-Type-Options = %q", path, got)
		}
	}
}

// TestFaviconICOHoldsSeveralSizes: the point of the format is that Windows and
// older browsers pick a size out of it, so one holding a single image is a
// pointless file.
func TestFaviconICOHoldsSeveralSizes(t *testing.T) {
	raw, err := staticFS.ReadFile("static/icons/favicon.ico")
	if err != nil {
		t.Fatalf("read favicon.ico: %v", err)
	}
	if len(raw) < 6 {
		t.Fatal("the file is too short to be an icon")
	}
	if raw[0] != 0 || raw[1] != 0 || raw[2] != 1 || raw[3] != 0 {
		t.Fatalf("the header is %v, want an ICONDIR of type 1", raw[:4])
	}
	count := int(raw[4]) | int(raw[5])<<8
	if count < 2 {
		t.Errorf("the icon holds %d image(s); a browser has nothing to choose between", count)
	}
}

// --------------------------------------------------------------- helpers ----

func decodeImage(t *testing.T, name string) image.Image {
	t.Helper()
	raw, err := staticFS.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return img
}

func decodeConfig(t *testing.T, srv *Server, path string) image.Config {
	t.Helper()
	rec := get(t, srv, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", path, rec.Code)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return config
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}
