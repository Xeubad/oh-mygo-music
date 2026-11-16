package sharecard_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
	imageDraw "golang.org/x/image/draw"
	"mygo-music/internal/sharecard"
)

// 从生产 PNG 回读实际尺寸与二维码，避免只验证预览或二维码编码器本身。
func TestRenderPNGKeepsSizeAndQRCode(t *testing.T) {
	link := "https://example.com/song?id=42&source=original"
	data, err := sharecard.Render(sharecard.Card{Title: "A quiet evening", Artist: "The Polar Lights", URL: link,
		Lines: []sharecard.Line{{Text: "We walk beneath the northern sky", Translation: "我們走在北方的天空下"}}})
	if err != nil {
		t.Fatal(err)
	}
	image, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if image.Bounds().Dx() != 1200 || image.Bounds().Dy() != 1600 {
		t.Fatalf("image size: %v", image.Bounds())
	}
	binary, err := gozxing.NewBinaryBitmapFromImage(image)
	if err != nil {
		t.Fatal(err)
	}
	result, err := qrcode.NewQRCodeReader().Decode(binary, nil)
	if err != nil || result.GetText() != link {
		t.Fatalf("decoded QR: %v, %v", result, err)
	}
}

// 五种构图、两种尺寸的最终成品及受控缩图都能读出歌曲链接。
func TestRenderLayoutsKeepScannableQRCode(t *testing.T) {
	for _, template := range []string{"", "cinema", "vinyl", "polaroid", "ticket"} {
		for _, square := range []bool{false, true} {
			name := template + "-portrait"
			if square {
				name = template + "-square"
			}
			t.Run(name, func(t *testing.T) {
				card := frameCard(template, square)
				data, err := sharecard.Render(card)
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := png.Decode(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				wantHeight := 1600
				if square {
					wantHeight = 1200
				}
				if decoded.Bounds().Size() != image.Pt(1200, wantHeight) {
					t.Fatal(decoded.Bounds())
				}
				assertQR(t, decoded, card.URL)
				small := image.NewRGBA(image.Rect(0, 0, 600, wantHeight/2))
				imageDraw.CatmullRom.Scale(small, small.Bounds(), decoded, decoded.Bounds(), draw.Src, nil)
				var compressed bytes.Buffer
				if err := jpeg.Encode(&compressed, small, &jpeg.Options{Quality: 85}); err != nil {
					t.Fatal(err)
				}
				jpegImage, err := jpeg.Decode(&compressed)
				if err != nil {
					t.Fatal(err)
				}
				assertQR(t, jpegImage, card.URL)
			})
		}
	}
}

func assertQR(t *testing.T, img image.Image, want string) {
	t.Helper()
	binary, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		t.Fatal(err)
	}
	result, err := qrcode.NewQRCodeReader().Decode(binary, map[gozxing.DecodeHintType]interface{}{gozxing.DecodeHintType_TRY_HARDER: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.GetText() != want {
		t.Fatalf("QR = %q", result.GetText())
	}
}

func frameCard(template string, square bool) sharecard.Card {
	cover := image.NewRGBA(image.Rect(0, 0, 600, 600))
	for row := range 600 {
		for column := range 600 {
			shade := color.RGBA{uint8(36 + row/7), uint8(68 + row/5), uint8(88 + row/8), 255}
			if row > 340+column/6 {
				shade = color.RGBA{33, uint8(61 + (row-340)/10), 65, 255}
			}
			if (column-410)*(column-410)+(row-178)*(row-178) < 54*54 {
				shade = color.RGBA{246, 221, 171, 255}
			}
			cover.SetRGBA(column, row, shade)
		}
	}
	return sharecard.Card{Title: "北方的夜 · Northern Lights", Artist: "The Polar Lights / 星野", URL: "https://example.com/song/northern-lights?from=share", Template: template, Square: square, Cover: cover,
		Lines: []sharecard.Line{{Text: "让风慢慢经过", Translation: "Let the evening wind pass softly."}}}
}

// 空歌词仍可导出，超容量返回明确错误而不是截断的 PNG。
func TestRenderEmptyAndOverflow(t *testing.T) {
	for _, template := range []string{"", "cinema", "vinyl", "polaroid", "ticket"} {
		card := sharecard.Card{Title: "An instrumental", Template: template, Square: true}
		if _, err := sharecard.Render(card); err != nil {
			t.Fatalf("empty %s: %v", template, err)
		}
		card.Lines = []sharecard.Line{{Text: strings.Repeat("A long lyric ", 200)}}
		data, err := sharecard.Render(card)
		if !errors.Is(err, sharecard.ErrCapacity) || len(data) != 0 {
			t.Fatalf("overflow %s: %d bytes, %v", template, len(data), err)
		}
	}
}

// 方形卡片原先被封面和过大的二维码挤占，三段双语歌词也无法正常分享。
func TestRenderThreeBilingualLinesFitEveryTemplate(t *testing.T) {
	for _, template := range []string{"", "cinema", "vinyl", "polaroid", "ticket"} {
		card := frameCard(template, true)
		card.Lines = []sharecard.Line{
			{Text: "让风慢慢经过", Translation: "Let the evening wind pass softly."},
			{Text: "你会听见远方", Translation: "You will hear the world beyond."},
			{Text: "我们一起出发", Translation: "We will find our way together."},
		}
		data, err := sharecard.Render(card)
		if err != nil {
			t.Fatalf("%q: %v", template, err)
		}
		decoded, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		assertQR(t, decoded, card.URL)
	}
}

// 歌曲信息也参与容量计算，异常长的元数据不能在底部被悄悄裁掉。
func TestRenderRejectsOverflowingMetadata(t *testing.T) {
	for _, template := range []string{"", "cinema", "vinyl", "polaroid", "ticket"} {
		card := frameCard(template, true)
		card.Title = strings.Repeat("A very long song title ", 100)
		data, err := sharecard.Render(card)
		if !errors.Is(err, sharecard.ErrCapacity) || len(data) != 0 {
			t.Fatalf("%q metadata overflow: %d bytes, %v", template, len(data), err)
		}
	}
}

// MM_FRAMES 下导出生产图片供人工视觉检查，不用像素快照断言布局。
func TestShareCardFrames(t *testing.T) {
	directory := os.Getenv("MM_FRAMES")
	if directory == "" {
		t.Skip("MM_FRAMES is not set")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	var artwork image.Image
	if path := os.Getenv("MM_SHARE_COVER"); path != "" {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		artwork, _, err = image.Decode(file)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, template := range []string{"", "cinema", "vinyl", "polaroid", "ticket"} {
		for _, square := range []bool{false, true} {
			name := template
			if name == "" {
				name = "minimal"
			}
			if square {
				name += "-square"
			} else {
				name += "-portrait"
			}
			card := frameCard(template, square)
			if artwork != nil {
				card.Cover = artwork
			}
			data, err := sharecard.Render(card)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if err := os.WriteFile(filepath.Join(directory, name+".png"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			card.Lines = []sharecard.Line{
				{Text: "让风慢慢经过", Translation: "Let the evening wind pass softly."},
				{Text: "你会听见远方", Translation: "You will hear the world beyond."},
				{Text: "我们一起出发", Translation: "We will find our way together."},
			}
			data, err = sharecard.Render(card)
			if err != nil {
				t.Fatalf("%s three lines: %v", name, err)
			}
			if err := os.WriteFile(filepath.Join(directory, name+"-three-lines.png"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			card.Cover = nil
			data, err = sharecard.Render(card)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, name+"-no-cover.png"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			card.Cover, card.Lines, card.URL = nil, nil, ""
			data, err = sharecard.Render(card)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, name+"-empty.png"), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
