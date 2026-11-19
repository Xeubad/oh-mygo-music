package player_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"mygo-music/internal/music"
)

func qualityFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// 只切夹具的完整 ADTS 帧；期望档位来自独立测量，不在断言里重算产品公式。
func adtsPrefix(t *testing.T, b []byte, packets int) []byte {
	t.Helper()
	off := 0
	for range packets {
		if off+7 > len(b) || b[off] != 0xff {
			t.Fatal("ADTS fixture changed")
		}
		n := int(b[off+3]&3)<<11 | int(b[off+4])<<3 | int(b[off+5]>>5)
		if n < 7 || off+n > len(b) {
			t.Fatal("ADTS fixture packet changed")
		}
		off += n
	}
	return bytes.Clone(b[:off])
}

// 调整夹具中的单个 box，同时更新祖先长度；不会重建或重新编码音频载荷。
func replaceQualityBox(t *testing.T, b []byte, kind string, payload []byte) []byte {
	t.Helper()
	var walk func([]byte) ([]byte, bool)
	walk = func(boxes []byte) ([]byte, bool) {
		for off := 0; off+8 <= len(boxes); {
			n := int(binary.BigEndian.Uint32(boxes[off:]))
			if n < 8 || n > len(boxes)-off {
				t.Fatal("fixture box changed")
			}
			name := string(boxes[off+4 : off+8])
			body, found := payload, name == kind
			if !found && (name == "moov" || name == "trak" || name == "mdia" || name == "minf" || name == "stbl") {
				body, found = walk(boxes[off+8 : off+n])
			}
			if found {
				out := append([]byte(nil), boxes[:off+8]...)
				binary.BigEndian.PutUint32(out[off:], uint32(8+len(body)))
				out = append(out, body...)
				return append(out, boxes[off+n:]...), true
			}
			off += n
		}
		return nil, false
	}
	out, found := walk(b)
	if !found {
		t.Fatalf("missing fixture box %s", kind)
	}
	return out
}

func TestCooperativeAACQualityBoundaries(t *testing.T) {
	adts := qualityFixture(t, "seek.aac")
	forged := qualityFixture(t, "qs-64k.m4a")
	at := bytes.Index(forged, []byte("btrt"))
	if at < 0 {
		t.Fatal("missing bitrate box")
	}
	binary.BigEndian.PutUint32(forged[at+8:], 320000)
	binary.BigEndian.PutUint32(forged[at+12:], 320000)
	at = bytes.Index(forged, []byte("tfhd")) + 20
	if at < 20 || binary.BigEndian.Uint32(forged[at:]) != 1024 {
		t.Fatal("default packet duration changed")
	}
	binary.BigEndian.PutUint32(forged[at:], 256)
	// 采样表故意把前90包指向 free，不能只统计后面的 mdat 而跳过低质/非法前缀。
	outside := qualityFixture(t, "seek-aac.m4a")
	mdat := bytes.Index(outside, []byte("mdat"))
	stsz := bytes.Index(outside, []byte("stsz"))
	if mdat < 4 || stsz < 0 || binary.BigEndian.Uint32(outside[stsz+12:]) != 131 {
		t.Fatal("MP4 packet table changed")
	}
	end := mdat - 4 + int(binary.BigEndian.Uint32(outside[mdat-4:]))
	split := mdat + 4
	for i := range 90 {
		split += int(binary.BigEndian.Uint32(outside[stsz+16+i*4:]))
	}
	copy(outside[mdat:mdat+4], "free")
	binary.BigEndian.PutUint32(outside[mdat-4:], uint32(split-(mdat-4)))
	binary.BigEndian.PutUint32(outside[split:], uint32(end-split))
	copy(outside[split+4:split+8], "mdat")
	shortDuration := qualityFixture(t, "seek-aac.m4a")
	stts := bytes.Index(shortDuration, []byte("stts"))
	if stts < 0 || binary.BigEndian.Uint32(shortDuration[stts+8:]) != 2 {
		t.Fatal("duration table changed")
	}
	binary.BigEndian.PutUint32(shortDuration[stts+16:], 1)
	binary.BigEndian.PutUint32(shortDuration[stts+24:], 1)
	// 播放顺序不变，只把两个 chunk 的物理顺序对调，实际AAC包仍与原夹具完全相同。
	reordered := qualityFixture(t, "seek-aac.m4a")
	payload := mdat + 4
	mid := payload
	for i := range 65 {
		mid += int(binary.BigEndian.Uint32(reordered[stsz+16+i*4:]))
	}
	data := append(bytes.Clone(reordered[mid:end]), reordered[payload:mid]...)
	copy(reordered[payload:end], data)
	stco := binary.BigEndian.AppendUint32(nil, 0)
	for _, v := range []uint32{2, uint32(payload + end - mid), uint32(payload)} {
		stco = binary.BigEndian.AppendUint32(stco, v)
	}
	reordered = replaceQualityBox(t, reordered, "stco", stco)
	stsc := binary.BigEndian.AppendUint32(nil, 0)
	for _, v := range []uint32{2, 1, 65, 1, 2, 66, 1} {
		stsc = binary.BigEndian.AppendUint32(stsc, v)
	}
	reordered = replaceQualityBox(t, reordered, "stsc", stsc)
	corrupt := adtsPrefix(t, adts, 64)
	for i := 7; i < len(adtsPrefix(t, adts, 1)); i++ {
		corrupt[i] = 0
	}
	unsupported := adtsPrefix(t, adts, 64)
	unsupported[2] &^= 0xc0 // 声明 AAC Main，不能套用 LC 的1024帧依据。
	tail := adtsPrefix(t, adts, 65)
	tail = tail[:len(tail)-10]
	for _, tc := range []struct {
		name, minimum string
		media         []byte
		accepted      bool
	}{
		{"low-adts", "128k", qualityFixture(t, "qs-64k.aac"), false},
		{"low-m4a", "128k", qualityFixture(t, "qs-64k.m4a"), false},
		{"forged-duration-and-bitrate", "128k", forged, false},
		{"packets-outside-media-data", "32k", outside, false},
		{"short-container-duration-not-evidence", "128k", shortDuration, true},
		{"physical-chunk-order-is-not-playback-order", "128k", reordered, true},
		{"too-few-complete-packets", "32k", adtsPrefix(t, adts, 31), false},
		{"corrupt-complete-packet", "32k", corrupt, false},
		{"unsupported-profile", "32k", unsupported, false},
		{"truncated-tail-with-valid-prefix", "64k", tail, true},
		{"short-prefix-not-promoted", "128k", tail, false},
		{"measured-low-quality-allowed", "32k", qualityFixture(t, "qs-64k.m4a"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "media")
			if err := os.WriteFile(path, tc.media, 0o600); err != nil {
				t.Fatal(err)
			}
			// 候选声明满足门槛，确保拒绝来自实测，而不是计划阶段提前过滤。
			src := cooperativePlugin(t, fmt.Sprintf(`
export function playbackPlan(){return {minimumQuality:%q,candidates:[{id:"one",platform:"qs",trackID:"raw",quality:%q}]};}
export function resolveCandidate(){return {stream:{url:%q,type:"flac",quality:"flac",bitRate:999}};}
`, tc.minimum, tc.minimum, "file:///"+filepath.ToSlash(path)))
			p, _ := cooperativePlayer(src)
			song, err := p.FindSong(src, music.Track{ID: "one", Source: "cooperative"}, "standard")
			if song != nil {
				defer song.Close()
			}
			if tc.accepted && (err != nil || song == nil) {
				t.Fatalf("有效完整前缀被拒绝: %v", err)
			}
			if !tc.accepted && (err == nil || song != nil) {
				t.Fatalf("不足或虚报的质量被放行: song=%v err=%v", song, err)
			}
		})
	}
}
