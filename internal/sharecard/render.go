package sharecard

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode"

	imageDraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"rsc.io/qr"
)

var ErrCapacity = errors.New("sharecard: content does not fit")

// Card 是一次导出的内容快照，独立于播放状态和任何窗口。
type Card struct {
	Title, Artist, URL string
	Template           string
	Square             bool
	Cover              image.Image
	Lines              []Line
}

// PageURL 只接受来源给出的网页地址；播放地址或文件路径不能用作缺省替代。
func PageURL(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.Scheme != "https" && parsed.Scheme != "http" || strings.ContainsFunc(raw, unicode.IsControl) {
		return ""
	}
	return raw
}

var cardFonts = sync.OnceValue(func() [2]*opentype.Font {
	var fonts [2]*opentype.Font
	for index := range fonts {
		var paths []string
		switch runtime.GOOS {
		case "windows":
			root := os.Getenv("WINDIR")
			if root == "" {
				root = `C:\Windows`
			}
			name := "msyh.ttc"
			if index == 1 {
				name = "msyhbd.ttc"
			}
			paths = []string{filepath.Join(root, "Fonts", name), filepath.Join(root, "Fonts", "YuGothM.ttc")}
		case "darwin":
			paths = []string{"/System/Library/Fonts/PingFang.ttc", "/System/Library/Fonts/Supplemental/Arial Unicode.ttf"}
		default:
			paths = []string{"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc", "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"}
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			collection, err := opentype.ParseCollection(data)
			if err != nil {
				continue
			}
			fonts[index], err = collection.Font(0)
			if err == nil {
				break
			}
		}
		if fonts[index] == nil {
			data := goregular.TTF
			if index == 1 {
				data = gobold.TTF
			}
			fonts[index], _ = opentype.Parse(data)
		}
	}
	return fonts
})

type faceKey struct {
	size int
	bold bool
}
type cardPainter struct {
	image *image.RGBA
	faces map[faceKey]font.Face
}

func (painter *cardPainter) face(size int, bold bool) font.Face {
	key := faceKey{size, bold}
	if face := painter.faces[key]; face != nil {
		return face
	}
	index := 0
	if bold {
		index = 1
	}
	face, _ := opentype.NewFace(cardFonts()[index], &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingFull})
	painter.faces[key] = face
	return face
}

func (painter *cardPainter) close() {
	for _, face := range painter.faces {
		face.Close()
	}
}

func (painter *cardPainter) fill(rect image.Rectangle, shade color.Color) {
	draw.Draw(painter.image, rect, image.NewUniform(shade), image.Point{}, draw.Src)
}

func wrap(text string, face font.Face, width int) []string {
	var result []string
	for _, paragraph := range strings.Split(text, "\n") {
		runes := []rune(paragraph)
		if len(runes) == 0 {
			result = append(result, "")
			continue
		}
		for len(runes) > 0 {
			end, space := 0, 0
			for end < len(runes) {
				if font.MeasureString(face, string(runes[:end+1])).Ceil() > width && end > 0 {
					break
				}
				end++
				if unicode.IsSpace(runes[end-1]) {
					space = end
				}
			}
			if end < len(runes) && space > 0 {
				end = space
			}
			result = append(result, strings.TrimRightFunc(string(runes[:end]), unicode.IsSpace))
			runes = runes[end:]
		}
	}
	return result
}

func (painter *cardPainter) text(text string, size int, bold bool, left, top, width int, shade color.Color, center bool) int {
	face := painter.face(size, bold)
	lineHeight := max(size*3/2, face.Metrics().Height.Ceil())
	for _, line := range wrap(text, face, width) {
		x := left
		if center {
			x += (width - font.MeasureString(face, line).Ceil()) / 2
		}
		drawer := font.Drawer{Dst: painter.image, Src: image.NewUniform(shade), Face: face, Dot: fixed.P(x, top+face.Metrics().Ascent.Ceil())}
		drawer.DrawString(line)
		top += lineHeight
	}
	return top
}

func (painter *cardPainter) cover(source image.Image, rect image.Rectangle) {
	if source == nil {
		painter.fill(rect, color.NRGBA{195, 204, 195, 255})
		return
	}
	bounds := source.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return
	}
	width, height := bounds.Dx(), bounds.Dy()
	if width*rect.Dy() > height*rect.Dx() {
		cropped := height * rect.Dx() / rect.Dy()
		bounds.Min.X += (width - cropped) / 2
		bounds.Max.X = bounds.Min.X + cropped
	} else {
		cropped := width * rect.Dy() / rect.Dx()
		bounds.Min.Y += (height - cropped) / 2
		bounds.Max.Y = bounds.Min.Y + cropped
	}
	imageDraw.CatmullRom.Scale(painter.image, rect, source, bounds, draw.Over, nil)
}

type cardLayout struct {
	top, bottom, left, width, size, translationSize int
	center, bold, shadow                            bool
	ink, muted                                      color.NRGBA
}

func (painter *cardPainter) textHeight(text string, size int, bold bool, width int) int {
	face := painter.face(size, bold)
	return len(wrap(text, face, width)) * max(size*3/2, face.Metrics().Height.Ceil())
}

func (painter *cardPainter) lyricBlock(lines []Line, layout cardLayout, top int, paint bool) int {
	for index, line := range lines {
		if index > 0 {
			top += 24
		}
		size, bold := layout.size, layout.bold
		if line.Text == "…" {
			size, bold = 30, false
		}
		if paint {
			if layout.shadow {
				painter.text(line.Text, size, bold, layout.left+1, top+3, layout.width, color.NRGBA{0, 0, 0, 170}, layout.center)
			}
			painter.text(line.Text, size, bold, layout.left, top, layout.width, layout.ink, layout.center)
		}
		top += painter.textHeight(line.Text, size, bold, layout.width)
		if line.Translation != "" {
			top += 9
			if paint {
				if layout.shadow {
					painter.text(line.Translation, layout.translationSize, false, layout.left+1, top+2, layout.width, color.NRGBA{0, 0, 0, 160}, layout.center)
				}
				painter.text(line.Translation, layout.translationSize, false, layout.left, top, layout.width, layout.muted, layout.center)
			}
			top += painter.textHeight(line.Translation, layout.translationSize, false, layout.width)
		}
	}
	return top
}

func typography(template string, theme cardTheme) cardLayout {
	layout := cardLayout{left: 100, width: 1000, size: 54, translationSize: 27, center: true, bold: true, ink: theme.ink, muted: theme.muted}
	switch template {
	case "cinema":
		layout.size, layout.translationSize, layout.center, layout.shadow = 50, 28, true, true
		layout.ink, layout.muted = color.NRGBA{255, 244, 216, 255}, color.NRGBA{222, 227, 224, 255}
	case "vinyl":
		layout.size = 46
		layout.ink, layout.muted = color.NRGBA{244, 245, 237, 255}, color.NRGBA{178, 193, 190, 255}
	case "polaroid":
		layout.left, layout.width, layout.size, layout.translationSize = 112, 976, 44, 26
		layout.center, layout.bold = true, false
	case "ticket":
		layout.left, layout.width, layout.size, layout.translationSize = 120, 960, 46, 26
		layout.center = false
	}
	return layout
}

func (painter *cardPainter) footerMetrics(card Card, qrSize int) (left, width, height int) {
	left, width = 88, 1024
	if card.Template == "ticket" || card.Template == "polaroid" {
		left, width = 112, 976
	}
	if qrSize > 0 {
		width -= qrSize + 44
	}
	height = max(116, qrSize, painter.textHeight(card.Title, 34, true, width)+8+painter.textHeight(card.Artist, 24, false, width))
	return left, width, height
}

func (painter *cardPainter) footer(card Card, layout cardLayout, top, qrSize int, theme cardTheme) {
	left, width, _ := painter.footerMetrics(card, qrSize)
	titleSize, artistSize := 34, 24
	block := painter.textHeight(card.Title, titleSize, true, width) + 8 + painter.textHeight(card.Artist, artistSize, false, width)
	height := painter.image.Bounds().Dy()
	y := top + max(0, (height-64-top-block)/2)
	if card.Template == "ticket" {
		for column := 88; column < 1112; column += 26 {
			painter.overlay(image.Rect(column, top-35, column+12, top-33), alpha(theme.muted, 100))
		}
		painter.rounded(image.Rect(22, top-62, 78, top-6), 28, theme.paper)
		painter.rounded(image.Rect(1122, top-62, 1178, top-6), 28, theme.paper)
	} else if card.Template != "cinema" {
		painter.overlay(image.Rect(left, top-30, 1200-left, top-29), alpha(layout.muted, 60))
	}
	end := painter.text(card.Title, titleSize, true, left, y, width, layout.ink, false)
	painter.text(card.Artist, artistSize, false, left, end+8, width, layout.muted, false)
}

func (painter *cardPainter) composition(card Card, height, qrSize, lyricHeight int, layout cardLayout, theme cardTheme) cardLayout {
	_, _, footerHeight := painter.footerMetrics(card, qrSize)
	footer := height - 64 - footerHeight
	layout.bottom = footer - 68
	painter.ambient(card.Cover, theme.paper, false)
	switch card.Template {
	case "cinema":
		painter.fill(painter.image.Bounds(), color.NRGBA{11, 15, 17, 255})
		frame := image.Rect(0, 70, 1200, footer-42)
		painter.artwork(card.Cover, frame, 0)
		painter.cinematicShade(frame, theme.dark, 0)
		layout.top, layout.bottom = 140, frame.Max.Y-54
	case "vinyl":
		painter.ambient(card.Cover, theme.dark, true)
		side := 474
		if card.Square {
			side = 310
		}
		if lyricHeight == 0 {
			side = min(650, footer-210)
		}
		artTop := 112
		if card.Square {
			artTop = 72
		}
		artTop += max(0, (layout.bottom-artTop-side-52-lyricHeight)/3)
		artLeft := (1200 - side*17/10) / 2
		painter.record(card.Cover, artLeft+side*6/5, artTop+side/2, side/2)
		sleeve := image.Rect(artLeft, artTop, artLeft+side, artTop+side)
		painter.shadow(sleeve, 4, 26)
		painter.artwork(card.Cover, sleeve, 4)
		painter.sleeveLight(sleeve)
		layout.top = sleeve.Max.Y + 52
	case "polaroid":
		paper := image.Rect(48, 36, 1152, height-28)
		painter.shadow(paper, 6, 20)
		painter.rounded(paper, 6, color.NRGBA{253, 253, 249, 255})
		photoHeight := min(790, layout.bottom-88-lyricHeight-74)
		if card.Square {
			photoHeight = min(540, photoHeight)
		}
		if lyricHeight == 0 {
			photoHeight = min(1024, layout.bottom-118)
		}
		photo := image.Rect(88, 78, 1112, 78+max(220, photoHeight))
		painter.artwork(card.Cover, photo, 1)
		painter.overlay(image.Rect(photo.Min.X, photo.Max.Y-2, photo.Max.X, photo.Max.Y), color.NRGBA{14, 25, 22, 30})
		layout.top = photo.Max.Y + 40
	case "ticket":
		painter.fill(painter.image.Bounds(), theme.paper)
		paper := image.Rect(50, 42, 1150, height-30)
		painter.shadow(paper, 24, 16)
		painter.rounded(paper, 24, color.NRGBA{253, 251, 245, 255})
		bannerHeight := min(570, layout.bottom-66-lyricHeight-96)
		if lyricHeight == 0 {
			bannerHeight = layout.bottom - 124
		}
		banner := image.Rect(74, 66, 1126, 66+max(224, bannerHeight))
		painter.artwork(card.Cover, banner, 13)
		painter.cinematicShade(banner, theme.dark, 13)
		layout.top = banner.Max.Y + 58
	default:
		side, artTop := 624, 88
		if card.Square {
			side, artTop = 400, 70
		}
		if lyricHeight == 0 {
			side = min(730, footer-210)
		}
		side = min(side, max(280, layout.bottom-artTop-64-lyricHeight))
		artTop += max(0, (layout.bottom-artTop-side-64-lyricHeight)/3)
		art := image.Rect((1200-side)/2, artTop, (1200+side)/2, artTop+side)
		painter.shadow(art, 18, 24)
		painter.rounded(art.Inset(-10), 24, color.NRGBA{255, 255, 255, 200})
		painter.artwork(card.Cover, art, 16)
		layout.top = art.Max.Y + 54
	}
	painter.grain(0.7)
	painter.footer(card, layout, footer, qrSize, theme)
	return layout
}

// Render 直接生成最终分辨率 PNG，预览也使用这些像素，不依赖测试器或桌面截图。
func Render(card Card) ([]byte, error) {
	height := 1600
	if card.Square {
		height = 1200
	}
	painter := &cardPainter{image: image.NewRGBA(image.Rect(0, 0, 1200, height)), faces: make(map[faceKey]font.Face)}
	defer painter.close()
	var code *qr.Code
	qrSize := 0
	if link := PageURL(card.URL); link != "" {
		var err error
		code, err = qr.Encode(link, qr.M)
		if err != nil {
			return nil, err
		}
		code.Scale = max(2, 192/(code.Size+8))
		qrSize = (code.Size + 8) * code.Scale
	}
	theme := colorsFromCover(card.Cover)
	layout := typography(card.Template, theme)
	// 先测量全部文字再分配画幅；字号和原文保持不变，过量内容不能被静默裁切。
	lyricHeight := painter.lyricBlock(card.Lines, layout, 0, false)
	_, _, footerHeight := painter.footerMetrics(card, qrSize)
	if lyricHeight > height-420 || footerHeight > height-420 {
		return nil, ErrCapacity
	}
	layout = painter.composition(card, height, qrSize, lyricHeight, layout, theme)
	if lyricHeight > layout.bottom-layout.top || layout.top < 0 {
		return nil, ErrCapacity
	}
	space := layout.bottom - layout.top - lyricHeight
	top := layout.top + space/4
	if card.Template == "cinema" {
		top = layout.bottom - lyricHeight
		if lyricHeight > 0 {
			// 选句变多时字幕会向上延伸，暗部必须一起跟随，避免亮色人像吞掉译文。
			painter.subtitleShade(image.Rect(0, max(70, top-140), 1200, layout.bottom+54), top)
		}
	}
	painter.lyricBlock(card.Lines, layout, top, true)
	if code != nil {
		right := 1112
		if card.Template == "ticket" || card.Template == "polaroid" {
			right = 1088
		}
		left, qrTop := right-qrSize, height-64-qrSize
		painter.rounded(image.Rect(left-5, qrTop-5, right+5, qrTop+qrSize+5), 10, color.NRGBA{255, 255, 255, 255})
		// 逐模块整数缩放并保留四格静区；此版本 qr.Image 的 At 不处理 Scale。
		for row := range code.Size {
			for column := range code.Size {
				if code.Black(column, row) {
					x, y := left+(column+4)*code.Scale, qrTop+(row+4)*code.Scale
					painter.fill(image.Rect(x, y, x+code.Scale, y+code.Scale), color.Black)
				}
			}
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, painter.image); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
