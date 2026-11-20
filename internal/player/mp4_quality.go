package player

import (
	"encoding/binary"
	"errors"
	"io"
	"math"

	"github.com/egoist/mygo/audio/mp4"
	aacpcm "github.com/tphakala/go-aac/pcm"
)

var errQualityLimit = errors.New("player: quality observation limit")
var errQualityPacket = errors.New("player: invalid quality packet")

func mp4Quality(dl *download) (string, string, int) {
	file := &downloadFile{dl: dl}
	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return "", "", 0
	}
	track, err := mp4.ReadTrack(file, size)
	if err != nil {
		return "", "", 0
	}
	if track.Codec == "alac" || track.Codec == "fLaC" {
		d, err := mp4.NewDecoder(file)
		if err != nil {
			return "", "", 0
		}
		var out [2048]float32
		n, err := d.Read(out[:])
		if n <= 0 || (err != nil && !errors.Is(err, io.EOF)) {
			return "", "", 0
		}
		for _, sample := range out[:n] {
			if math.IsNaN(float64(sample)) || math.IsInf(float64(sample), 0) {
				return "", "", 0
			}
		}
		// 无损取实际编码与可解码样本为证，保存格式仍是 M4A，不能冒充原生 FLAC。
		return "lossless", "m4a", 0
	}
	if track.Codec != "mp4a" {
		return "", "", 0
	}
	config, err := aacpcm.ParseASC(track.Config)
	if err != nil || config.ObjectType != 2 || config.SBR || config.PS {
		return "", "", 0
	}
	fd, err := aacpcm.NewRawDecoder(track.Config)
	if err != nil {
		return "", "", 0
	}
	r := &mp4QualityReader{downloadFile: file, size: size, fd: fd, rate: config.SampleRate, channels: config.Channels}
	d, err := mp4.NewDecoder(r)
	if err != nil || d.SampleRate() != config.SampleRate {
		return "", "", 0
	}
	// 独立解码器复用正式拆包路径，不移动播放位置；构造期间的元数据读取不计载荷。
	r.active = true
	var out [2048]float32
	for r.packets < qualityAACPackets {
		before := r.packets
		n, err := d.Read(out[:])
		if r.invalid {
			return "", "", 0
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			// 容器可把播放长度截短；只要仍读到新的完整包，继续观察而不信声明时长。
			if r.packets > before {
				continue
			}
			break
		}
		if errors.Is(err, errQualityLimit) {
			break
		}
		if err != nil || n <= 0 {
			return "", "", 0
		}
	}
	quality, kbps := aacPayloadQuality(r.bytes, r.packets, config.SampleRate)
	if quality == "" {
		return "", "", 0
	}
	return quality, "m4a", kbps
}

// shortcut: MyGo v0.9.0 每次 ReadAt 读取一个完整压缩包；升级拆包器时须复验普通及分片夹具。
// 只观察顶层 mdat 内的读取，避免把 moov／moof／传输头当成码率；不复制采样表解析器。
type mp4QualityBox struct {
	start, end, head int64
	kind             string
}

type mp4QualityReader struct {
	*downloadFile
	fd                    *aacpcm.FrameDecoder
	size, next            int64
	boxes                 []mp4QualityBox
	bytes, packets, reads int
	metadata              int
	rate, channels        int
	active, invalid       bool
	pcm                   []byte
}

func (r *mp4QualityReader) ReadAt(b []byte, off int64) (nread int, failure error) {
	if !r.active {
		return r.downloadFile.ReadAt(b, off)
	}
	defer func() {
		// 分片解析器会吞掉某些读取错误，必须保留失败，不能用先前好包掩盖非法采样位置。
		if failure != nil && !errors.Is(failure, io.EOF) && !errors.Is(failure, io.ErrUnexpectedEOF) && !errors.Is(failure, errQualityLimit) {
			r.invalid = true
		}
	}()
	r.reads++
	if r.invalid || r.reads > 4096 || off < 0 || off > r.size || len(b) == 0 || int64(len(b)) > r.size-off {
		return 0, errQualityPacket
	}
	// 分片解码器可能先看下一片的头再读当前包，所以保留已发现的载荷区间。
	for r.next <= off {
		if len(r.boxes) >= 1024 || r.next > r.size-8 {
			return 0, errQualityPacket
		}
		var h [16]byte
		if _, err := r.downloadFile.ReadAt(h[:8], r.next); err != nil {
			return 0, err
		}
		n, head := int64(binary.BigEndian.Uint32(h[:4])), int64(8)
		if n == 1 {
			if _, err := r.downloadFile.ReadAt(h[8:], r.next+8); err != nil {
				return 0, err
			}
			large := binary.BigEndian.Uint64(h[8:])
			if large > uint64(r.size-r.next) {
				return 0, errQualityPacket
			}
			n, head = int64(large), 16
		} else if n == 0 {
			n = r.size - r.next
		}
		if n < head || n > r.size-r.next {
			return 0, errQualityPacket
		}
		r.boxes = append(r.boxes, mp4QualityBox{r.next, r.next + n, head, string(h[4:8])})
		r.next += n
	}
	payload, metadata := false, false
	for _, box := range r.boxes {
		if off < box.start || off >= box.end {
			continue
		}
		if int64(len(b)) > box.end-off {
			return 0, errQualityPacket
		}
		payload = box.kind == "mdat" && off >= box.start+box.head
		// Read阶段仅允许拆包器的顶层头和完整moof读取，free等区的“音频包”必须拒绝。
		metadata = (off == box.start && len(b) == 8) ||
			(box.head == 16 && off == box.start+8 && len(b) == 8) ||
			(box.kind == "moof" && off == box.start+box.head && int64(len(b)) == box.end-off)
		break
	}
	if !payload {
		if !metadata || len(b) > (1<<20)-r.metadata {
			return 0, errQualityPacket
		}
		r.metadata += len(b)
		return r.downloadFile.ReadAt(b, off)
	}
	if r.packets >= qualityAACPackets || len(b) > qualityAACBytes-r.bytes {
		return 0, errQualityLimit
	}
	n, err := r.downloadFile.ReadAt(b, off)
	if err != nil || n != len(b) {
		return n, err
	}
	pcm, frames, err := r.fd.DecodeFrame(r.pcm[:0], b)
	if err != nil || frames != 1024 || r.fd.SampleRate() != r.rate || r.fd.Channels() != r.channels {
		return 0, errQualityPacket
	}
	r.pcm = pcm
	r.bytes += n
	r.packets++
	return n, nil
}
