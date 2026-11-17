package player

import (
	"time"

	"github.com/egoist/mygo/audio"
)

// 音频设备是可替换的系统边界；无头行为测试逐帧拉取解码结果，不启动真实声卡。
type audioOutput interface {
	Play()
	Pause()
	Close()
	Seek(time.Duration)
	Position() time.Duration
	Duration() time.Duration
	Playing() bool
	OnEnd(func()) func()
	SetVolume(float64) *audio.Player
}
