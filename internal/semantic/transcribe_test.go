package semantic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func fakeWhisper(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.NotFound(w, r)

			return
		}

		if err := r.ParseMultipartForm(64 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		f, hdr, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		_, _ = io.Copy(io.Discard, f)

		calls.Add(1)

		_ = json.NewEncoder(w).Encode(map[string]string{
			"text": " part " + hdr.Filename + "\n model=" + r.FormValue("model") + " prompt=" + r.FormValue("prompt") + " ",
		})
	}))
}

func TestTranscribe(t *testing.T) {
	var calls atomic.Int32

	srv := fakeWhisper(t, &calls)
	defer srv.Close()

	root := t.TempDir()
	media := filepath.Join(root, "talk.mp3")

	if err := os.WriteFile(media, []byte("ID3fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	tr, err := NewTranscriber(TranscribeConfig{BaseURL: srv.URL + "/v1", OutDir: filepath.Join(root, "out"), FFmpeg: "-"})
	if err != nil {
		t.Fatal(err)
	}

	paths := tr.TranscribeAll(context.Background(), []string{media, filepath.Join(root, "missing.mp3")}, "")
	if len(paths) != 1 {
		t.Fatalf("paths = %v", paths)
	}

	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}

	want := "part talk.mp3\nmodel=whisper-1 prompt=" + FallbackPrompt
	if string(raw) != want {
		t.Fatalf("transcript = %q, want %q", raw, want)
	}

	// Cached on the second call.
	tr.TranscribeAll(context.Background(), []string{media}, "")

	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}

	// Unsupported container without ffmpeg fails.
	mkv := filepath.Join(root, "clip.mkv")
	_ = os.WriteFile(mkv, []byte("x"), 0o644)

	if _, err := tr.Transcribe(context.Background(), mkv, ""); err == nil || !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("expected ffmpeg error, got %v", err)
	}
}

func TestTranscribeSplit(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	var calls atomic.Int32

	srv := fakeWhisper(t, &calls)
	defer srv.Close()

	root := t.TempDir()
	media := filepath.Join(root, "tone.mkv")

	// 150 s of tone; at 32 kbit/s with a 300 KB limit this splits into
	// 60 s segments.
	gen := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi",
		"-i", "sine=frequency=440:duration=150", "-c:a", "libvorbis", media)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot generate audio: %v %s", err, out)
	}

	tr, err := NewTranscriber(TranscribeConfig{BaseURL: srv.URL + "/v1", OutDir: filepath.Join(root, "out"), MaxUpload: 300_000})
	if err != nil {
		t.Fatal(err)
	}

	p, err := tr.Transcribe(context.Background(), media, "hint")
	if err != nil {
		t.Fatal(err)
	}

	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}

	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "part part_0002.mp3") || !strings.Contains(string(raw), "prompt=hint") {
		t.Fatalf("transcript = %q", raw)
	}
}

func TestBuildWhisperPrompt(t *testing.T) {
	if got := BuildWhisperPrompt(nil); got != FallbackPrompt {
		t.Fatalf("got %q", got)
	}

	got := BuildWhisperPrompt([]string{"a", "", "b", "c", "d", "e", "f"})
	if got != "Technical discussion about a, b, c, d, e. Use proper punctuation and paragraph breaks." {
		t.Fatalf("got %q", got)
	}
}
