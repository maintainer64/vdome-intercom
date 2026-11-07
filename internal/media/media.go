// Package media converts account-scoped RTSP streams into local HLS. Source
// credentials are passed on stdin, never in process arguments or HTTP replies.
package media

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var ErrUnavailable = errors.New("видеопоток недоступен")

type StartError struct{ Reason string }

func (e *StartError) Error() string {
	return "Камера: " + e.Reason + ". Можно запросить запасной кадр вручную."
}

// Capture only a bounded private stderr tail; return fixed descriptions, never URLs.
type diagnostics struct {
	mu   sync.Mutex
	tail string
}

func (d *diagnostics) Write(b []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tail += string(b)
	if len(d.tail) > 4096 {
		d.tail = d.tail[len(d.tail)-4096:]
	}
	return len(b), nil
}
func (d *diagnostics) failure() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	text := strings.ToLower(d.tail)
	reason := "источник не прислал видеосегменты"
	for _, pair := range [][2]string{{"401 unauthorized", "источник отклонил авторизацию (401)"}, {"403 forbidden", "источник отказал в доступе (403)"}, {"429 too many", "источник ограничил частоту подключений (429)"}, {"453 not enough", "источник ограничил число подключений (453)"}, {"timed out", "истекло время ожидания источника"}, {"connection refused", "источник отклонил соединение"}, {"invalid data", "источник вернул неподдерживаемые данные"}} {
		if strings.Contains(text, pair[0]) {
			reason = pair[1]
			break
		}
	}
	return &StartError{Reason: reason}
}

var ErrLimit = errors.New("достигнут лимит одновременных видеопотоков")

type process struct {
	id, owner, device, dir string
	diagnostics            *diagnostics
	cancel                 context.CancelFunc
	done                   chan struct{}
	last                   time.Time
}
type Manager struct {
	mu      sync.Mutex
	streams map[string]*process
	root    string
	closed  bool
	stop    chan struct{}
	wg      sync.WaitGroup
}

func New() *Manager { return &Manager{streams: map[string]*process{}, stop: make(chan struct{})} }
func randomID() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func concatInput(source string) (string, error) {
	u, err := url.Parse(source)
	if err != nil || u.Hostname() == "" || strings.ContainsAny(source, "\r\n\x00") || (u.Scheme != "rtsp" && u.Scheme != "rtsps" && u.Scheme != "http" && u.Scheme != "https") {
		return "", ErrUnavailable
	}
	// ffconcat quoting rules: close the quote, escape ', reopen the quote.
	input := "ffconcat version 1.0\nfile '" + strings.ReplaceAll(source, "'", "'\\''") + "'\n"
	if u.Scheme == "rtsp" || u.Scheme == "rtsps" {
		input += "option rtsp_transport tcp\noption timeout 10000000\n"
	} else {
		input += "option rw_timeout 10000000\n"
	}
	return input, nil
}
func (m *Manager) Start(ctx context.Context, owner, device, source string) (string, error) {
	input, err := concatInput(source)
	if err != nil {
		return "", err
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", errors.New("для видеопотока нужен установленный ffmpeg")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", ErrUnavailable
	}
	for _, p := range m.streams {
		if p.owner == owner && p.device == device {
			p.last = time.Now()
			m.mu.Unlock()
			return waitReady(ctx, p)
		}
	}
	if len(m.streams) >= 4 {
		m.mu.Unlock()
		return "", ErrLimit
	}
	if m.root == "" {
		m.root, err = os.MkdirTemp("", "vdome-media-")
		if err != nil {
			m.mu.Unlock()
			return "", ErrUnavailable
		}
		go m.reap()
	}
	dir, err := os.MkdirTemp(m.root, "stream-")
	if err != nil {
		m.mu.Unlock()
		return "", ErrUnavailable
	}
	processCtx, cancel := context.WithCancel(context.Background())
	p := &process{id: randomID(), owner: owner, device: device, dir: dir, cancel: cancel, done: make(chan struct{}), last: time.Now(), diagnostics: &diagnostics{}}
	cmd := exec.CommandContext(processCtx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-f", "concat", "-safe", "0", "-protocol_whitelist", "pipe,rtsp,rtsps,tcp,tls,udp,rtp,http,https,crypto", "-i", "pipe:0", "-map", "0:v:0", "-an", "-c:v", "copy", "-f", "hls", "-hls_time", "2", "-hls_list_size", "4", "-hls_flags", "delete_segments+temp_file+independent_segments", "-hls_segment_filename", filepath.Join(dir, "segment-%09d.ts"), filepath.Join(dir, "index.m3u8"))
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = io.Discard
	cmd.Stderr = p.diagnostics
	if err := cmd.Start(); err != nil {
		cancel()
		os.RemoveAll(dir)
		m.mu.Unlock()
		return "", ErrUnavailable
	}
	m.streams[p.id] = p
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		_ = cmd.Wait()
		close(p.done)
		m.mu.Lock()
		if m.streams[p.id] == p {
			delete(m.streams, p.id)
		}
		m.mu.Unlock()
		_ = os.RemoveAll(dir)
	}()
	id, err := waitReady(ctx, p)
	if err != nil {
		cancel()
	}
	return id, err
}
func waitReady(ctx context.Context, p *process) (string, error) {
	deadline := time.NewTimer(24 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if b, err := os.ReadFile(filepath.Join(p.dir, "index.m3u8")); err == nil && strings.Contains(string(b), "#EXTINF:") {
			return p.id, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", p.diagnostics.failure()
		case <-p.done:
			return "", p.diagnostics.failure()
		case <-tick.C:
		}
	}
}

var filePattern = regexp.MustCompile(`^(index\.m3u8|segment-[0-9]{9}\.ts)$`)

func (m *Manager) Read(owner, id, file string) ([]byte, string, error) {
	if !filePattern.MatchString(file) {
		return nil, "", ErrUnavailable
	}
	m.mu.Lock()
	p := m.streams[id]
	if p == nil || p.owner != owner {
		m.mu.Unlock()
		return nil, "", ErrUnavailable
	}
	p.last = time.Now()
	dir := p.dir
	m.mu.Unlock()
	b, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil || len(b) > 16<<20 {
		return nil, "", ErrUnavailable
	}
	mime := "video/mp2t"
	if file == "index.m3u8" {
		mime = "application/vnd.apple.mpegurl"
	}
	return b, mime, nil
}
func (m *Manager) Stop(owner, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.streams[id]; p != nil && p.owner == owner {
		p.cancel()
		delete(m.streams, id)
	}
}
func (m *Manager) StopOwner(owner string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, p := range m.streams {
		if p.owner == owner {
			p.cancel()
			delete(m.streams, id)
		}
	}
}
func (m *Manager) reap() {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-tick.C:
			m.mu.Lock()
			for id, p := range m.streams {
				if time.Since(p.last) > 45*time.Second {
					p.cancel()
					delete(m.streams, id)
				}
			}
			m.mu.Unlock()
		}
	}
}
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	close(m.stop)
	active := []*process{}
	for _, p := range m.streams {
		p.cancel()
		active = append(active, p)
	}
	root := m.root
	m.mu.Unlock()
	for _, p := range active {
		<-p.done
	}
	m.wg.Wait()
	if root != "" {
		_ = os.RemoveAll(root)
	}
}
