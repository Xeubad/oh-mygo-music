package player

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"time"

	"mygo-music/internal/automix"
)

func (p *Player) prepareAdaptiveAutoMix() {
	preparation, output := p.transitionNext, p.transition
	if preparation == nil || output == nil || !preparation.ready || preparation.autoMixDone ||
		preparation.autoMixPlanning || p.stream == nil || preparation.stream == nil {
		return
	}
	current, next, currentID, position := output.analysisPair(preparation.id)
	if current == nil || next == nil {
		return
	}
	oldDownload, _ := p.stream.source()
	newDownload, _ := preparation.stream.source()
	if !analysisDownloadReady(oldDownload) || !analysisDownloadReady(newDownload) {
		p.autoMixStatus = "waiting"
		return
	}
	preparation.autoMixPlanning = true
	p.autoMixStatus = "analyzing"
	generation := p.gen
	parent := preparation.context
	if parent == nil {
		parent = context.Background()
	}
	engine := p.AutoMixEngine
	var advisor automix.Planner
	if p.AutoMixAdvisor != nil {
		advisor = p.AutoMixAdvisor()
	}
	adviceContext, adviceCancel := context.WithCancel(parent)
	p.autoMixCloudCancel = adviceCancel
	p.autoMixCloudEpoch++
	adviceEpoch := p.autoMixCloudEpoch
	earliest := current.absoluteSourceFrame(position) + 2*automix.SampleRate
	oldGain, nextGain := 1.0, 1.0
	if p.Normalize != nil && p.Normalize() {
		oldGain, nextGain = normGain(p.info), normGain(preparation.info)
	}
	ctx, cancel := context.WithTimeout(parent, 230*time.Second)
	preparation.autoMixRenderCancel = cancel
	go func() {
		defer cancel()
		defer adviceCancel()
		directory, err := os.MkdirTemp("", "mygo-automix-pcm-*")
		if err == nil {
			defer os.RemoveAll(directory)
		}
		var result *automix.AdaptiveResult
		if err == nil {
			var oldPath, nextPath string
			oldPath, err = snapshotAnalysisWAV(ctx, oldDownload, directory)
			if err == nil {
				nextPath, err = snapshotAnalysisWAV(ctx, newDownload, directory)
			}
			if err == nil {
				result, err = engine.Render(ctx, automix.AdaptiveJob{
					Current: oldPath, Next: nextPath, Earliest: earliest, GainRatio: oldGain / nextGain,
					Advisor: advisor, AdvisorContext: adviceContext,
				})
			}
		}
		p.post(func() {
			if p.gen != generation || p.transition != output || p.transitionNext != preparation ||
				parent.Err() != nil || !p.transitionSettings().AutoMix {
				return
			}
			preparation.autoMixPlanning, preparation.autoMixDone = false, true
			if err != nil || result == nil || result.Validate() != nil {
				p.autoMixStatus = "fallback"
				return
			}
			if result.AISelected && p.autoMixCloudEpoch != adviceEpoch {
				preparation.autoMixDone = false
				p.autoMixStatus = "waiting"
				p.prepareAdaptiveAutoMix()
				return
			}
			if output.installAdaptive(currentID, preparation.id, result) {
				p.autoMixStatus = "local"
				if result.AISelected {
					p.autoMixStatus = "ai"
				}
			} else {
				// 结果到达时已经过了接点，保留原曲和原待接流，不把过期方案硬塞进输出。
				p.autoMixStatus = "fallback"
			}
		})
	}()
}

func analysisDownloadReady(download *download) bool {
	if download == nil {
		return false
	}
	download.mu.Lock()
	defer download.mu.Unlock()
	return download.done && download.err == nil && download.n > 0 && !download.closed
}

func snapshotAnalysisWAV(ctx context.Context, source *download, directory string) (path string, failure error) {
	if !source.share() {
		return "", io.ErrClosedPipe
	}
	defer source.release()
	raw, err := os.CreateTemp(directory, "encoded-*")
	if err != nil {
		return "", err
	}
	rawPath := raw.Name()
	defer os.Remove(rawPath)
	defer raw.Close()
	source.mu.Lock()
	length, complete := source.n, source.done && source.err == nil
	source.mu.Unlock()
	if !complete || length > 256<<20 {
		return "", errors.New("player: analysis source incomplete or too large")
	}
	chunk := make([]byte, 64<<10)
	for offset := int64(0); offset < length; {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		source.mu.Lock()
		count, readErr := source.readAt(chunk[:min(int64(len(chunk)), length-offset)], offset)
		source.mu.Unlock()
		if readErr != nil && readErr != io.EOF || count == 0 {
			return "", io.ErrUnexpectedEOF
		}
		if _, err = raw.Write(chunk[:count]); err != nil {
			return "", err
		}
		offset += int64(count)
	}
	if _, err = raw.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	cloned := opened(raw, length)
	decoded, err := openStream(cloned, 0)
	if err != nil {
		cloned.Close()
		return "", err
	}
	resampled := newTransitionDecoder(decoded)
	defer resampled.Close()
	wave, err := os.CreateTemp(directory, "pcm-*.wav")
	if err != nil {
		return "", err
	}
	defer wave.Close()
	header := make([]byte, 44)
	copy(header, "RIFF")
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 3)
	binary.LittleEndian.PutUint16(header[22:], 2)
	binary.LittleEndian.PutUint32(header[24:], automix.SampleRate)
	binary.LittleEndian.PutUint32(header[28:], automix.SampleRate*8)
	binary.LittleEndian.PutUint16(header[32:], 8)
	binary.LittleEndian.PutUint16(header[34:], 32)
	copy(header[36:], "data")
	if _, err = wave.Write(header); err != nil {
		return "", err
	}
	samples := make([]float32, 8192)
	bytes := make([]byte, len(samples)*4)
	var total int64
	for {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		count, readErr := resampled.Read(samples)
		if count%2 != 0 || total+int64(count) > 1200*automix.SampleRate*2 {
			return "", errors.New("player: analysis PCM exceeds bounds")
		}
		for index, value := range samples[:count] {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return "", errors.New("player: invalid decoded PCM")
			}
			binary.LittleEndian.PutUint32(bytes[index*4:], math.Float32bits(value))
		}
		if _, err = wave.Write(bytes[:count*4]); err != nil {
			return "", err
		}
		total += int64(count)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
		if count == 0 {
			return "", io.ErrNoProgress
		}
	}
	binary.LittleEndian.PutUint32(header[4:], uint32(total*4+36))
	binary.LittleEndian.PutUint32(header[40:], uint32(total*4))
	_, err = wave.WriteAt(header, 0)
	return wave.Name(), err
}

func (buffer *transitionBuffer) absoluteSourceFrame(frame int64) int64 {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.mapping != nil {
		return buffer.mapping.SourceFrame(frame)
	}
	if decoded, ok := buffer.decoder.(*transitionDecoder); ok {
		return frame + decoded.origin
	}
	return frame
}

func (buffer *transitionBuffer) absoluteOutputFrame(frame int64) int64 {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.mapping != nil {
		return buffer.mapping.OutputFrame(frame)
	}
	if decoded, ok := buffer.decoder.(*transitionDecoder); ok {
		return frame - decoded.origin
	}
	return frame
}

func (output *transitionOutput) installAdaptive(currentID, nextID uint64, result *automix.AdaptiveResult) bool {
	if result.Validate() != nil {
		return false
	}
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.closed || output.current == nil || output.current.id != currentID ||
		output.next == nil || output.next.id != nextID || output.tail != nil {
		return false
	}
	current, currentOK := output.current.decoder.(*transitionBuffer)
	next, nextOK := output.next.decoder.(*transitionBuffer)
	if !currentOK || !nextOK {
		return false
	}
	start, end := current.absoluteOutputFrame(result.Start), current.absoluteOutputFrame(result.End)
	if start <= output.current.pos || end <= start {
		return false
	}
	mapping := automix.Mapping{Anchors: result.Anchors}
	if !next.installIntro(result.Samples, mapping, int(result.Consumed*2)) {
		return false
	}
	current.mu.Lock()
	current.length = result.CurrentFrames
	if decoded, ok := current.decoder.(*transitionDecoder); ok && current.mapping == nil {
		current.length -= decoded.origin
	}
	current.mu.Unlock()
	next.mu.Lock()
	next.length = result.NextFrames
	next.mu.Unlock()
	_, output.current.length = current.lengths()
	_, output.next.length = next.lengths()
	output.autoMixPlan = &automix.Candidate{Start: start, End: end, Entry: result.Entry,
		Ratio: result.Ratio, Profiles: []string{"rendered"}}
	return true
}
