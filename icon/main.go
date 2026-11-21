// Command icon sizes the supplied XEU artwork for both desktop platforms.
// Run it from the repository root with go run ./icon.
package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"

	"golang.org/x/image/draw"
)

func main() {
	data, err := os.ReadFile("icon/xeu-logo.png")
	if err != nil {
		log.Fatal(err)
	}
	logo, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		log.Fatal(err)
	}
	bounds := visibleBounds(logo)
	if bounds.Empty() {
		log.Fatal("the logo is transparent")
	}

	var appIcon bytes.Buffer
	if err := png.Encode(&appIcon, fitLogo(logo, bounds, 1024, 920)); err != nil {
		log.Fatal(err)
	}
	for _, name := range []string{"resources/icon.png", "resources/icon-dev.png", "website/public/icon.png"} {
		if err := os.WriteFile(name, appIcon.Bytes(), 0o644); err != nil {
			log.Fatal(err)
		}
	}
	tray := fitLogo(logo, bounds, 64, 60)
	if err := savePNG("internal/app/tray/tray.png", tray); err != nil {
		log.Fatal(err)
	}
	// The menu bar needs the same mark as a monochrome template; keep the
	// source here so `go run ./icon` regenerates every packaged icon.
	template := fitLogo(logo, bounds, 36, 32)
	for y := range template.Bounds().Dy() {
		for x := range template.Bounds().Dx() {
			p := template.NRGBAAt(x, y)
			template.SetNRGBA(x, y, color.NRGBA{A: p.A})
		}
	}
	if err := savePNG("internal/app/tray/tray-template.png", template); err != nil {
		log.Fatal(err)
	}
}

// visibleBounds trims only transparent surroundings, preserving antialiasing.
func visibleBounds(logo image.Image) image.Rectangle {
	var bounds image.Rectangle
	for y := logo.Bounds().Min.Y; y < logo.Bounds().Max.Y; y++ {
		for x := logo.Bounds().Min.X; x < logo.Bounds().Max.X; x++ {
			if _, _, _, alpha := logo.At(x, y).RGBA(); alpha != 0 {
				bounds = bounds.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return bounds
}

func fitLogo(logo image.Image, bounds image.Rectangle, size, maxSide int) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	scale := max(bounds.Dx(), bounds.Dy())
	width := max(1, bounds.Dx()*maxSide/scale)
	height := max(1, bounds.Dy()*maxSide/scale)
	left, top := (size-width)/2, (size-height)/2
	draw.CatmullRom.Scale(out, image.Rect(left, top, left+width, top+height), logo, bounds, draw.Src, nil)
	return out
}

func savePNG(name string, img image.Image) error {
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		return err
	}
	return os.WriteFile(name, data.Bytes(), 0o644)
}
