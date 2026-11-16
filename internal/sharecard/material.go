package sharecard

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	imageDraw "golang.org/x/image/draw"
)

type cardTheme struct {
	paper, dark, accent, ink, muted color.NRGBA
}

func blend(first, second color.NRGBA, amount float64) color.NRGBA {
	return color.NRGBA{
		R: uint8(float64(first.R)*(1-amount) + float64(second.R)*amount),
		G: uint8(float64(first.G)*(1-amount) + float64(second.G)*amount),
		B: uint8(float64(first.B)*(1-amount) + float64(second.B)*amount), A: 255,
	}
}

func alpha(shade color.NRGBA, opacity uint8) color.NRGBA {
	shade.A = opacity
	return shade
}

func colorsFromCover(source image.Image) cardTheme {
	base := color.NRGBA{66, 100, 106, 255}
	if source != nil && !source.Bounds().Empty() {
		var red, green, blue, weight float64
		bounds := source.Bounds()
		for row := range 16 {
			for column := range 16 {
				pixel := color.NRGBAModel.Convert(source.At(bounds.Min.X+(2*column+1)*bounds.Dx()/32, bounds.Min.Y+(2*row+1)*bounds.Dy()/32)).(color.NRGBA)
				// 有颜色的像素权重更高，白色边框不会把整张卡片的氛围冲成灰白。
				saturation := float64(max(pixel.R, pixel.G, pixel.B)-min(pixel.R, pixel.G, pixel.B)) / 255
				current := (0.3 + saturation) * float64(pixel.A) / 255
				red += float64(pixel.R) * current
				green += float64(pixel.G) * current
				blue += float64(pixel.B) * current
				weight += current
			}
		}
		if weight > 0 {
			base = color.NRGBA{uint8(red / weight), uint8(green / weight), uint8(blue / weight), 255}
		}
	}
	return cardTheme{
		paper:  blend(base, color.NRGBA{250, 251, 248, 255}, 0.91),
		dark:   blend(base, color.NRGBA{10, 16, 22, 255}, 0.86),
		accent: blend(base, color.NRGBA{205, 221, 203, 255}, 0.42),
		ink:    color.NRGBA{31, 40, 42, 255}, muted: color.NRGBA{103, 116, 115, 255},
	}
}

func (painter *cardPainter) overlay(rect image.Rectangle, shade color.NRGBA) {
	draw.Draw(painter.image, rect, image.NewUniform(shade), image.Point{}, draw.Over)
}

func roundedMask(width, height, radius int) *image.Alpha {
	mask := image.NewAlpha(image.Rect(0, 0, width, height))
	radius = min(radius, width/2, height/2)
	for row := range height {
		for column := range width {
			distanceX := math.Max(float64(radius)-float64(column)-0.5, float64(column)+0.5-float64(width-radius))
			distanceY := math.Max(float64(radius)-float64(row)-0.5, float64(row)+0.5-float64(height-radius))
			coverage := 1.0
			if distanceX > 0 && distanceY > 0 {
				coverage = math.Max(0, math.Min(1, float64(radius)+0.5-math.Hypot(distanceX, distanceY)))
			}
			mask.Pix[row*mask.Stride+column] = uint8(coverage * 255)
		}
	}
	return mask
}

func (painter *cardPainter) rounded(rect image.Rectangle, radius int, shade color.NRGBA) {
	if rect.Empty() {
		return
	}
	mask := roundedMask(rect.Dx(), rect.Dy(), radius)
	draw.DrawMask(painter.image, rect, image.NewUniform(shade), image.Point{}, mask, image.Point{}, draw.Over)
}

func (painter *cardPainter) shadow(rect image.Rectangle, radius, spread int) {
	for layer := 8; layer > 0; layer-- {
		distance := spread * layer / 8
		painter.rounded(rect.Inset(-distance).Add(image.Pt(0, spread/3)), radius+distance, color.NRGBA{10, 22, 25, 4})
	}
}

func (painter *cardPainter) artwork(source image.Image, rect image.Rectangle, radius int) {
	if rect.Empty() {
		return
	}
	canvas := &cardPainter{image: image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))}
	if source == nil || source.Bounds().Empty() {
		canvas.placeholder()
	} else {
		canvas.cover(source, canvas.image.Bounds())
	}
	if radius == 0 {
		draw.Draw(painter.image, rect, canvas.image, image.Point{}, draw.Over)
		return
	}
	mask := roundedMask(rect.Dx(), rect.Dy(), radius)
	draw.DrawMask(painter.image, rect, canvas.image, image.Point{}, mask, image.Point{}, draw.Over)
}

func (painter *cardPainter) placeholder() {
	bounds := painter.image.Bounds()
	for row := range bounds.Dy() {
		shade := blend(color.NRGBA{56, 83, 97, 255}, color.NRGBA{130, 154, 140, 255}, float64(row)/float64(bounds.Dy()))
		painter.fill(image.Rect(0, row, bounds.Dx(), row+1), shade)
	}
	side := min(bounds.Dx(), bounds.Dy()) / 3
	center := image.Pt(bounds.Dx()/2, bounds.Dy()/2)
	for _, radius := range []int{side, side * 4 / 5, side * 3 / 5} {
		painter.rounded(image.Rect(center.X-radius, center.Y-radius, center.X+radius, center.Y+radius), radius, color.NRGBA{227, 237, 226, 22})
	}
	painter.rounded(image.Rect(center.X-side/6, center.Y-side/6, center.X+side/6, center.Y+side/6), side/6, color.NRGBA{232, 241, 228, 175})
}

func (painter *cardPainter) ambient(source image.Image, shade color.NRGBA, dark bool) {
	painter.fill(painter.image.Bounds(), shade)
	if source == nil || source.Bounds().Empty() {
		return
	}
	// 在小画幅上模糊再放大，保留封面的环境色而不复制可辨识的背景细节。
	thumb := &cardPainter{image: image.NewRGBA(image.Rect(0, 0, 30, 40))}
	thumb.cover(source, thumb.image.Bounds())
	for range 2 {
		blurred := image.NewRGBA(thumb.image.Bounds())
		for row := range 40 {
			for column := range 30 {
				var red, green, blue, count int
				for offsetY := -4; offsetY <= 4; offsetY++ {
					for offsetX := -4; offsetX <= 4; offsetX++ {
						pixel := thumb.image.RGBAAt(max(0, min(29, column+offsetX)), max(0, min(39, row+offsetY)))
						red, green, blue, count = red+int(pixel.R), green+int(pixel.G), blue+int(pixel.B), count+1
					}
				}
				blurred.SetRGBA(column, row, color.RGBA{uint8(red / count), uint8(green / count), uint8(blue / count), 255})
			}
		}
		thumb.image = blurred
	}
	imageDraw.BiLinear.Scale(painter.image, painter.image.Bounds(), thumb.image, thumb.image.Bounds(), draw.Src, nil)
	opacity := uint8(234)
	if dark {
		opacity = 228
	}
	painter.overlay(painter.image.Bounds(), alpha(shade, opacity))
}

func (painter *cardPainter) cinematicShade(rect image.Rectangle, shade color.NRGBA, radius int) {
	if rect.Empty() {
		return
	}
	gradient := image.NewNRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	for row := range rect.Dy() {
		progress := float64(row) / float64(rect.Dy())
		opacity := 0.06 + 0.9*math.Pow(progress, 2.5)
		draw.Draw(gradient, image.Rect(0, row, rect.Dx(), row+1), image.NewUniform(alpha(shade, uint8(255*opacity))), image.Point{}, draw.Src)
	}
	draw.DrawMask(painter.image, rect, gradient, image.Point{}, roundedMask(rect.Dx(), rect.Dy(), radius), image.Point{}, draw.Over)
}

func (painter *cardPainter) sleeveLight(rect image.Rectangle) {
	for column := rect.Min.X; column < rect.Max.X; column++ {
		position := float64(column-rect.Min.X) / float64(rect.Dx())
		opacity := 7 + 17*math.Exp(-math.Pow((position-0.06)*45, 2))
		painter.overlay(image.Rect(column, rect.Min.Y+2, column+1, rect.Max.Y-2), color.NRGBA{255, 255, 255, uint8(opacity)})
	}
}

func (painter *cardPainter) subtitleShade(rect image.Rectangle, textTop int) {
	for row := rect.Min.Y; row < rect.Max.Y; row++ {
		fade := math.Min(1, float64(row-rect.Min.Y)/float64(max(1, textTop-rect.Min.Y)))
		fade = fade * fade * (3 - 2*fade)
		painter.overlay(image.Rect(rect.Min.X, row, rect.Max.X, row+1), color.NRGBA{5, 11, 15, uint8(150 * fade)})
	}
}

func (painter *cardPainter) record(source image.Image, centerX, centerY, radius int) {
	bounds := image.Rect(centerX-radius, centerY-radius, centerX+radius, centerY+radius)
	painter.shadow(bounds, radius, 18)
	record := image.NewRGBA(image.Rect(0, 0, radius*2, radius*2))
	for row := range radius * 2 {
		for column := range radius * 2 {
			deltaX, deltaY := float64(column-radius), float64(row-radius)
			distance := math.Hypot(deltaX, deltaY)
			if distance > float64(radius) {
				continue
			}
			reflection := math.Pow(math.Abs((deltaX*0.65+deltaY*0.76)/math.Max(1, distance)), 18)
			groove := 2.0 * math.Sin(distance*2.8)
			shade := uint8(max(5.0, 12+reflection*30+groove))
			record.SetRGBA(column, row, color.RGBA{shade, shade + 2, shade + 3, 255})
		}
	}
	draw.Draw(painter.image, bounds, record, image.Point{}, draw.Over)
	label := radius / 3
	painter.artwork(source, image.Rect(centerX-label, centerY-label, centerX+label, centerY+label), label)
	painter.rounded(image.Rect(centerX-8, centerY-8, centerX+8, centerY+8), 8, color.NRGBA{12, 19, 20, 255})
}

func (painter *cardPainter) grain(amount float64) {
	for row := range painter.image.Bounds().Dy() {
		for column := range painter.image.Bounds().Dx() {
			// 固定坐标噪声让纸面有细微质感，预览和每次导出仍能保持一致。
			seed := uint32(column)*374761393 + uint32(row)*668265263
			seed = (seed ^ (seed >> 13)) * 1274126177
			delta := (float64(seed&255)/127.5 - 1) * amount
			offset := row*painter.image.Stride + column*4
			for channel := range 3 {
				painter.image.Pix[offset+channel] = uint8(max(0, min(255, float64(painter.image.Pix[offset+channel])+delta)))
			}
		}
	}
}
