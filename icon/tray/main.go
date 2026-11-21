// Command tray adapts the app icon to a monochrome macOS menu bar template.
//
// Run it from the repository root:
//
//	go run ./icon
//	go run ./icon/tray
//
// With -preview dir, it also writes the icon over light and dark menu bars,
// enlarged, to check by eye.
package main

import (
	"flag"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/image/draw"
)

func render(logo image.Image, size int) *image.NRGBA {
	bounds := image.Rect(0, 0, size, size)
	alpha := image.NewAlpha(bounds)
	draw.CatmullRom.Scale(alpha, bounds, logo, logo.Bounds(), draw.Src, nil)
	img := image.NewNRGBA(bounds)
	for y := range size {
		for x := range size {
			img.SetNRGBA(x, y, color.NRGBA{A: alpha.AlphaAt(x, y).A})
		}
	}
	return img
}

// preview puts the icon on a light menu bar and a dark one, tinted as
// macOS tints it, and enlarged with nearest pixels so each one shows.
func preview(icon *image.NRGBA, zoom int) *image.NRGBA {
	w := icon.Bounds().Dx()
	pad := w / 3
	side := w + 2*pad
	out := image.NewNRGBA(image.Rect(0, 0, side*2*zoom, side*zoom))
	grounds := []color.NRGBA{{236, 236, 238, 255}, {40, 40, 44, 255}}
	inks := []float64{0, 255}
	for g := range grounds {
		for y := 0; y < side; y++ {
			for x := 0; x < side; x++ {
				c := grounds[g]
				if ix, iy := x-pad, y-pad; ix >= 0 && iy >= 0 && ix < w && iy < w {
					a := float64(icon.NRGBAAt(ix, iy).A) / 255
					mix := func(v uint8) uint8 { return uint8(float64(v)*(1-a) + inks[g]*a) }
					c = color.NRGBA{mix(c.R), mix(c.G), mix(c.B), 255}
				}
				for dy := 0; dy < zoom; dy++ {
					for dx := 0; dx < zoom; dx++ {
						out.SetNRGBA((g*side+x)*zoom+dx, y*zoom+dy, c)
					}
				}
			}
		}
	}
	return out
}

func save(name string, img image.Image) {
	f, err := os.Create(name)
	if err != nil {
		log.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
}

func main() {
	out := flag.String("o", "internal/app/tray/tray-template.png", "where to write the icon")
	dir := flag.String("preview", "", "a directory for previews")
	flag.Parse()
	f, err := os.Open("resources/icon.png")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	logo, err := png.Decode(f)
	if err != nil {
		log.Fatal(err)
	}
	icon := render(logo, 36)
	save(*out, icon)
	if *dir != "" {
		save(filepath.Join(*dir, "preview-2x.png"), preview(icon, 8))
		save(filepath.Join(*dir, "preview-1x.png"), preview(render(logo, 18), 12))
		save(filepath.Join(*dir, "preview-large.png"), preview(render(logo, 360), 1))
	}
}
