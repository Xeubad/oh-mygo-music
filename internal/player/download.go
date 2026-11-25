package player

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"mygo-music/internal/music"
)

// Song is a track's audio found for the listener to keep a copy of, in a file
// of their own: kept in the cache, a file on disk, or at its source.
type Song struct {
	// Type is its file's: one of songTypes, as its source said.
	Type       string
	PlaybackID string        // 已核验录音身份供下载任务固定后续重解析。
	file       *os.File      // kept in the cache, or on disk
	st         *music.Stream // or else where it streams from
	prepared   stream
}

// FindSong finds track t of src at quality, to save, as the player would play
// it: kept in the cache, or as src streams it. It blocks, for src is asked; run
// it off the main thread, and close what it returns, or save it.
func (p *Player) FindSong(src music.Source, t music.Track, quality string) (*Song, error) {
	return p.FindSongContext(context.Background(), src, t, quality)
}

func (p *Player) FindSongContext(ctx context.Context, src music.Source, t music.Track, quality string) (*Song, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f, st, ok := p.Cache.find(t.Source, t.ID, quality); ok {
		if t.PlaybackID == "" || st.PlaybackID == t.PlaybackID {
			return &Song{Type: st.Type, PlaybackID: st.PlaybackID, file: f, st: st}, nil
		}
		f.Close()
	}
	if src == nil {
		return nil, errNoSource
	}
	st, err := src.Stream(t.ID, quality)
	if err != nil {
		return nil, err
	}
	if st == nil || st.URL == "" {
		return nil, music.ErrUnavailable
	}
	s := &Song{Type: songType(st.Type), PlaybackID: st.PlaybackID, st: st}
	if path, ok := music.PathOf(st.URL); ok {
		if s.file, err = os.Open(path); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// songTypes are the types of song the app knows, which name their files.
var songTypes = []string{"mp3", "flac", "ogg", "wav", "m4a", "aac"}

// songType is the type a stream says it is, to name its file by: one the app
// knows, or else mp3, as the cache takes it. What a source sent may hold
// anything.
func songType(typ string) string {
	if typ = strings.ToLower(typ); slices.Contains(songTypes, typ) {
		return typ
	}
	return "mp3"
}

// Close lets go of the song, unsaved.
func (s *Song) Close() {
	if s.prepared != nil {
		s.prepared.Close()
		s.prepared = nil
	}
	if s.file != nil {
		s.file.Close()
	}
}

// SaveTo writes the song to path, in place of what's there, and closes it. It
// comes into a file of its own beside path first, so one cut short leaves path
// as it was.
func (s *Song) SaveTo(path string) error {
	return s.SaveToContext(context.Background(), path)
}

// SaveToContext 允许分享取消时终止网络读取，不影响原有下载入口。
func (s *Song) SaveToContext(ctx context.Context, path string) error {
	defer s.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.part")
	if err != nil {
		return err
	}
	err = s.copyContext(ctx, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// copy writes all of the song to w.
func (s *Song) copy(w io.Writer) error {
	return s.copyContext(context.Background(), w)
}

func (s *Song) copyContext(ctx context.Context, w io.Writer) error {
	return s.write(ctx, w, nil)
}

// WriteDownload 仅负责写入音频；调用方负责关闭 Song 和提交临时文件。
func (s *Song) WriteDownload(ctx context.Context, w io.Writer, progress func(written, total int64)) error {
	return s.write(ctx, w, progress)
}

func (s *Song) write(ctx context.Context, w io.Writer, progress func(written, total int64)) error {
	copyAudio := func(r io.Reader, size int64) error {
		if progress == nil {
			_, err := io.Copy(w, r)
			return err
		}
		writer := &downloadProgressWriter{Writer: w, report: progress, total: size}
		written, err := io.Copy(writer, contextReader{ctx: ctx, reader: r})
		if err == nil && (written == 0 || size > 0 && written != size) {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	if s.prepared != nil {
		// 直接复制已验证候选的原始下载，不能重新请求临时 URL 导致版本或字节变化。
		download, _ := s.prepared.source()
		stop := context.AfterFunc(ctx, download.Close)
		defer stop()
		download.mu.Lock()
		size := download.size
		download.mu.Unlock()
		return copyAudio(contextReader{ctx: ctx, reader: &downloadReader{d: download}}, size)
	}
	if s.file != nil {
		var size int64
		if progress != nil {
			info, err := s.file.Stat()
			if err != nil {
				return err
			}
			size = info.Size()
		}
		return copyAudio(contextReader{ctx: ctx, reader: s.file}, size)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", s.st.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", music.Agent)
	for k, v := range s.st.Headers {
		req.Header.Set(k, v)
	}
	res, err := music.HeadersStay(http.DefaultClient, req.URL.Hostname(), s.st.Headers).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return errors.New(res.Status)
	}
	return copyAudio(res.Body, res.ContentLength)
}

type downloadProgressWriter struct {
	io.Writer
	report  func(written, total int64)
	total   int64
	written int64
}

func (w *downloadProgressWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if n > 0 {
		w.written += int64(n)
		w.report(w.written, w.total)
	}
	return n, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
