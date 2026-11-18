package automix

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type AdaptiveJob struct {
	Current   string
	Next      string
	Earliest  int64
	GainRatio float64
}

type AdaptiveRenderer interface {
	Render(context.Context, AdaptiveJob) (*AdaptiveResult, error)
}

type AdaptiveResult struct {
	Schema        string          `json:"schema"`
	Status        string          `json:"status"`
	Checks        map[string]bool `json:"checks"`
	Start         int64           `json:"start"`
	End           int64           `json:"end"`
	Entry         int64           `json:"entry"`
	Beats         int             `json:"beats"`
	Ratio         float64         `json:"ratio"`
	CurrentFrames int64           `json:"current_frames"`
	NextFrames    int64           `json:"next_frames"`
	Mixed         string          `json:"mixed"`
	Consumed      int64           `json:"consumed"`
	Anchors       []Anchor        `json:"mapping"`
	Samples       []float32       `json:"-"`
}

func (result *AdaptiveResult) Validate() error {
	invalid := errors.New("automix: rejected adaptive audio or time mapping")
	if result == nil || result.Schema != "adaptive-1" || result.Status != "accepted" ||
		result.Start < 0 || result.End <= result.Start || result.End > result.CurrentFrames ||
		result.CurrentFrames > 1200*SampleRate || result.NextFrames > 1200*SampleRate ||
		result.End-result.Start < 5*SampleRate/2 || result.End-result.Start > 22*SampleRate ||
		result.Entry < 0 || result.Entry > 35*SampleRate || result.Consumed > result.NextFrames ||
		math.IsNaN(result.Ratio) || result.Ratio < .94 || result.Ratio > 1.06 ||
		result.Beats < 4 || result.Beats > 48 {
		return invalid
	}
	for _, name := range []string{
		"finite_audio", "no_50ms_silent_windows", "old_voice_gain_at_last_vocal",
		"new_voice_gain_at_first_vocal", "vocal_handoff", "old_music_retires_at_new_voice",
		"old_mid_gain_at_new_voice", "outgoing_beat_grid", "incoming_beat_grid",
		"relative_beat_phase", "same_source_prefix",
	} {
		if !result.Checks[name] {
			return fmt.Errorf("automix: quality check rejected: %s", name)
		}
	}
	frames := int64(len(result.Samples) / 2)
	if len(result.Samples)%2 != 0 || frames < result.End-result.Start+SampleRate ||
		frames > 33*SampleRate || len(result.Anchors) < 2 || len(result.Anchors) > 256 {
		return invalid
	}
	for _, value := range result.Samples {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || math.Abs(float64(value)) > 4 {
			return invalid
		}
	}
	for index, anchor := range result.Anchors {
		if anchor.Output < 0 || anchor.Source < result.Entry || anchor.Source > result.Consumed {
			return invalid
		}
		if index > 0 {
			previous := result.Anchors[index-1]
			if anchor.Output <= previous.Output || anchor.Source <= previous.Source {
				return invalid
			}
			rate := float64(anchor.Source-previous.Source) / float64(anchor.Output-previous.Output)
			if rate < .75 || rate > 1.25 {
				return invalid
			}
		}
	}
	first, last := result.Anchors[0], result.Anchors[len(result.Anchors)-1]
	if first.Output != 0 || first.Source != result.Entry || last.Output != frames || last.Source != result.Consumed {
		return invalid
	}
	// 不信任子进程的“无断音”布尔值，Go 在安装前独立核对实际重叠音频。
	for start := int64(0); start+SampleRate/20 <= result.End-result.Start; start += SampleRate / 20 {
		var power float64
		for _, sample := range result.Samples[start*2 : (start+SampleRate/20)*2] {
			power += float64(sample) * float64(sample)
		}
		if power/float64(SampleRate/10) < 1e-10 {
			return invalid
		}
	}
	return nil
}

type LocalEngine struct {
	Root  string
	Cache string
	// Python 仅用于开发和无头验收；发行默认使用随包的独立 CPU 运行时。
	Python string
}

type boundedEngineLog struct {
	writer io.Writer
	left   int
}

func (log *boundedEngineLog) Write(data []byte) (int, error) {
	count := len(data)
	if log.left > 0 {
		kept := min(log.left, count)
		if _, err := log.writer.Write(data[:kept]); err != nil {
			return 0, err
		}
		log.left -= kept
	}
	return count, nil
}

func (engine *LocalEngine) Render(parent context.Context, job AdaptiveJob) (*AdaptiveResult, error) {
	if engine == nil || job.GainRatio < .01 || job.GainRatio > 100 || math.IsNaN(job.GainRatio) {
		return nil, errors.New("automix: invalid local job")
	}
	python := engine.Python
	if python == "" {
		python = filepath.Join(engine.Root, "python", "python.exe")
	}
	if err := os.MkdirAll(engine.Cache, 0700); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp(engine.Cache, "job-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	payload, err := json.Marshal(map[string]any{
		"schema": "adaptive-1", "current": job.Current, "next": job.Next,
		"earliest": float64(job.Earliest) / SampleRate, "gain_ratio": job.GainRatio,
		"cache": filepath.Join(engine.Cache, "analysis"), "output": directory,
	})
	if err != nil {
		return nil, err
	}
	jobPath := filepath.Join(directory, "job.json")
	if err = os.WriteFile(jobPath, payload, 0600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 220*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, python, filepath.Join(engine.Root, "worker.py"), "--job", jobPath)
	command.Dir = engine.Root
	quietEngineCommand(command)
	log, logErr := os.OpenFile(filepath.Join(engine.Cache, "last-run.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if logErr == nil {
		defer log.Close()
		writer := &boundedEngineLog{writer: log, left: 256 << 10}
		command.Stdout, command.Stderr = writer, writer
	}
	// 子进程无窗口、无音频设备，日志不包含 URL、凭据或原始 PCM。
	if err = command.Run(); err != nil {
		return nil, fmt.Errorf("automix: local engine failed: %w", err)
	}
	file, err := os.Open(filepath.Join(directory, "result.json"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if report, readErr := io.ReadAll(io.LimitReader(file, 1<<20)); readErr == nil {
		os.WriteFile(filepath.Join(engine.Cache, "last-result.json"), report, 0600)
		file.Seek(0, io.SeekStart)
	}
	var result AdaptiveResult
	if err = json.NewDecoder(io.LimitReader(file, 1<<20)).Decode(&result); err != nil {
		return nil, err
	}
	if result.Status != "accepted" {
		return nil, errors.New("automix: no safe adaptive transition")
	}
	// 只读本任务约定的文件，子进程不能让播放器读取任意路径。
	if filepath.Clean(result.Mixed) != filepath.Join(directory, "mixed.wav") {
		return nil, errors.New("automix: unexpected rendered audio path")
	}
	result.Samples, err = readAdaptiveWAV(result.Mixed)
	if err != nil {
		return nil, err
	}
	if err = result.Validate(); err != nil {
		return nil, err
	}
	return &result, nil
}

func readAdaptiveWAV(path string) ([]float32, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	header := make([]byte, 12)
	if _, err = io.ReadFull(file, header); err != nil || string(header[:4]) != "RIFF" || string(header[8:]) != "WAVE" {
		return nil, errors.New("automix: invalid WAV header")
	}
	valid := false
	for scanned := uint64(12); scanned < 32<<20; {
		chunk := make([]byte, 8)
		if _, err = io.ReadFull(file, chunk); err != nil {
			return nil, err
		}
		size := binary.LittleEndian.Uint32(chunk[4:])
		scanned += 8 + uint64(size)
		if size > 32<<20 || scanned > 32<<20 {
			return nil, errors.New("automix: oversized rendered WAV")
		}
		switch string(chunk[:4]) {
		case "fmt ":
			if size < 16 || size > 64 {
				return nil, errors.New("automix: invalid WAV format")
			}
			format := make([]byte, size)
			if _, err = io.ReadFull(file, format); err != nil {
				return nil, err
			}
			valid = binary.LittleEndian.Uint16(format) == 3 && binary.LittleEndian.Uint16(format[2:]) == 2 &&
				binary.LittleEndian.Uint32(format[4:]) == SampleRate && binary.LittleEndian.Uint16(format[14:]) == 32
		case "data":
			if !valid || size%8 != 0 {
				return nil, errors.New("automix: rendered PCM format mismatch")
			}
			payload := make([]byte, size)
			if _, err = io.ReadFull(file, payload); err != nil {
				return nil, err
			}
			samples := make([]float32, size/4)
			for index := range samples {
				samples[index] = math.Float32frombits(binary.LittleEndian.Uint32(payload[index*4:]))
			}
			return samples, nil
		default:
			if _, err = file.Seek(int64(size), io.SeekCurrent); err != nil {
				return nil, err
			}
		}
		if size%2 != 0 {
			if _, err = file.Seek(1, io.SeekCurrent); err != nil {
				return nil, err
			}
		}
	}
	return nil, errors.New("automix: missing rendered PCM")
}
