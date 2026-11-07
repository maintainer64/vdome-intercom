package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourceRejectsFilesAndManifestInjection(t *testing.T) {
	for _, source := range []string{"file:///etc/passwd", "pipe:0", "rtsp://", "rtsp://camera/stream\nfile /etc/passwd", "https://camera/stream\x00"} {
		if _, err := concatInput(source); err == nil {
			t.Fatalf("unsafe source accepted: %q", source)
		}
	}
	input, err := concatInput("rtsp://camera/stream?token=secret'quoted")
	if err != nil || !strings.Contains(input, "'\\''") || !strings.Contains(input, "option rtsp_transport tcp") {
		t.Fatal("invalid private ffconcat source")
	}
}

func TestStreamFilesAreOwnerScopedAndConfined(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.m3u8"), []byte("#EXTM3U\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := New()
	m.streams["stream"] = &process{id: "stream", owner: "owner", dir: dir, last: time.Now()}
	if _, _, err := m.Read("other-account", "stream", "index.m3u8"); err == nil {
		t.Fatal("cross-account stream read")
	}
	for _, name := range []string{"../index.m3u8", "/etc/passwd", "key", "index.m3u8.tmp", "segment-1.ts"} {
		if _, _, err := m.Read("owner", "stream", name); err == nil {
			t.Fatal("invalid path accepted")
		}
	}
	b, mime, err := m.Read("owner", "stream", "index.m3u8")
	if err != nil || mime != "application/vnd.apple.mpegurl" || string(b) != "#EXTM3U\n" {
		t.Fatal("valid playlist unavailable")
	}
}
