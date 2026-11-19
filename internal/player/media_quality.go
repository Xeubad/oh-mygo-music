package player

import (
	"errors"
	"fmt"
	"io"

	aacpcm "github.com/tphakala/go-aac/pcm"
)

// VBR 的头几帧可能偏低；观察最多约三秒的AAC-LC，不用容器时长或接口标称码率。
const qualityAACPackets = 128
const qualityAACMinimum = 32
const qualityAACBytes = 512 << 10

func lossyQuality(kbps int) string {
	for _, level := range []int{320, 256, 128, 64, 32} {
		if kbps >= level {
			return fmt.Sprintf("%dk", level)
		}
	}
	return ""
}

func aacPayloadQuality(bytes, packets, rate int) (string, int) {
	if packets < qualityAACMinimum || rate <= 0 {
		return "", 0
	}
	kbps := int(int64(bytes) * 8 * int64(rate) / (int64(packets) * 1024 * 1000))
	return lossyQuality(kbps), kbps
}

func adtsQuality(s *adtsStream) (string, string, int) {
	fd := aacpcm.NewADTSDecoder()
	var bytes, packets int
	var packet []byte
	for packets < qualityAACPackets {
		if packets >= len(s.offs) && !s.findMore() {
			break
		}
		var err error
		packet, err = readPacket(s.dl, s.offs[packets], s.sizes[packets], packet)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		} // 截断尾包不计入已经完整验证的前缀。
		if err != nil {
			return "", "", 0
		}
		length, rate, ok := adtsHeader(packet)
		if !ok || length != len(packet) || rate != s.rate || packet[2]>>6 != 1 || packet[6]&3 != 0 {
			return "", "", 0
		}
		head := 7
		if packet[1]&1 == 0 {
			head = 9
		}
		if len(packet) <= head {
			return "", "", 0
		}
		if bytes+len(packet)-head > qualityAACBytes {
			break
		}
		_, frames, err := fd.DecodeFrame(nil, packet)
		if err != nil || frames != 1024 || fd.SampleRate() != rate || fd.Channels() < 1 || fd.Channels() > 8 {
			return "", "", 0
		}
		bytes += len(packet) - head
		packets++
	}
	quality, kbps := aacPayloadQuality(bytes, packets, s.rate)
	if quality == "" {
		return "", "", 0
	}
	return quality, "aac", kbps
}
