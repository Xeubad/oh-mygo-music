package app

// 本文件的胶囊材质只在播放器内部使用，不修改 Windows 的全局透明设置。
// 不用 glass.Glass/Blur：它们读系统透明开关会画成实底。
// 直接用半透明填充 + 高光边，和队列面板的 Background 一样，真实窗口必然透。

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// forcedGlass 是半透明 + 高光边的胶囊材质，带按压生长与悬停亮边反馈。
type forcedGlass struct {
	interactive bool
}

// forcedGlassKey 区分按压与悬停动画的键，避免与 MyGo 内部动画串扰。
type forcedGlassKey struct{ role string }

var (
	forcedPressKey = forcedGlassKey{"press"}
	forcedHoverKey = forcedGlassKey{"hover"}
)

// forcedGlassState 携带按压生长量与悬停亮度，按帧重绘。
type forcedGlassState struct {
	forcedGlass
	grow, hover float32
}

// BuildMaterial 让交互胶囊按下时轻微生长、悬停时亮边，与系统玻璃控件一致。
func (g forcedGlass) BuildMaterial(e ui.Element) ui.Material {
	if !g.interactive {
		return g
	}
	press, hover := float32(0), float32(0)
	if e.Pressed() {
		press = 1
	}
	if e.Hovered() {
		hover = 1
	}
	return forcedGlassState{
		forcedGlass: g,
		grow:        e.Animate(forcedPressKey, press, 150*time.Millisecond),
		hover:       e.Animate(forcedHoverKey, hover, 120*time.Millisecond),
	}
}

func (g forcedGlass) PaintMaterial(p *ui.Painter, box ui.Rect, radii [4]float32) {
	g.paint(p, box, radii, 0)
}

func (g forcedGlassState) PaintMaterial(p *ui.Painter, box ui.Rect, radii [4]float32) {
	dx, dy := 1.1*g.grow, 0.45*g.grow
	box = ui.Rect{X: box.X - dx, Y: box.Y - dy, W: box.W + 2*dx, H: box.H + 2*dy}
	for i := range radii {
		if radii[i] > 0 {
			radii[i] += dy
		}
	}
	g.forcedGlass.paint(p, box, radii, g.hover)
}

// paint 画半透明填充与高光边，透出背后的封面；hover 越高，底和边越亮。
func (g forcedGlass) paint(p *ui.Painter, box ui.Rect, radii [4]float32, hover float32) {
	if box.W <= 0 || box.H <= 0 {
		return
	}
	bg, edge := ui.Hex("#ffffff").Alpha(0.42), ui.Hex("#ffffff").Alpha(0.32)
	if p.Theme().Dark {
		bg, edge = ui.Hex("#ffffff").Alpha(0.16), ui.Hex("#ffffff").Alpha(0.22)
	}
	if hover > 0 {
		bg = bg.Mix(white, 0.22*hover)
		edge = edge.Mix(white, 0.5*hover)
	}
	if p.Preferences().HighContrast {
		bg = p.Theme().Surface
	}
	p.FillRadii(box, radii, bg)
	p.Stroke(box, edge, max(radii[0], radii[1], radii[2], radii[3]), 1)
}
