package processor

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"testing"
	"time"

	"app/db"
	"app/internal/app/conns"
	"app/pkg/ai"
	"app/pkg/ffmpeg"
	"app/pkg/whisperx"

	"github.com/google/uuid"
)

func testWAV(dur time.Duration) []byte {
	const rate = 22050
	samples := int(dur.Seconds() * rate)
	data := make([]byte, samples*2)

	var buf bytes.Buffer
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(36+len(data)))
	buf.WriteString("WAVEfmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint32(rate))
	binary.Write(&buf, binary.LittleEndian, uint32(rate*2))
	binary.Write(&buf, binary.LittleEndian, uint16(2))
	binary.Write(&buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, uint32(len(data)))
	buf.Write(data)

	return buf.Bytes()
}

type fakeStreamEngine struct {
	chunks []ai.StreamChunk
}

func (e *fakeStreamEngine) TTS(ctx context.Context, text string, ref []byte) ([]byte, []whisperx.Timiing, error) {
	panic("batch TTS must not be reached when streaming succeeds")
}

func (e *fakeStreamEngine) TTSStream(ctx context.Context, text string, ref []byte, fn func(ai.StreamChunk) error) error {
	for _, c := range e.chunks {
		if err := fn(c); err != nil {
			return err
		}
	}
	return nil
}

// frameLog records emitted audio frames with receive timestamps.
type frameLog struct {
	mu     sync.Mutex
	frames []time.Time
}

func (l *frameLog) writer() conns.AudioWriter {
	return func(frame []byte) bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.frames = append(l.frames, time.Now())
		return true
	}
}

func (l *frameLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.frames)
}

func newGateTestService(t *testing.T, engine ai.TTSEngine) *Service {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not in PATH")
	}
	return &Service{
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ffmpeg:    ffmpeg.New(&ffmpeg.Config{TmpDir: t.TempDir()}),
		ttsEngine: engine,
	}
}

func streamChunks(n int, dur time.Duration) []ai.StreamChunk {
	chunks := make([]ai.StreamChunk, n)
	for i := range chunks {
		chunks[i] = ai.StreamChunk{
			Text:        "hello world",
			SpeechStart: time.Duration(i) * dur,
			SpeechEnd:   time.Duration(i+1) * dur,
			Audio:       testWAV(dur),
		}
	}
	return chunks
}

func TestPlayTTSStreamingGate(t *testing.T) {
	engine := &fakeStreamEngine{chunks: streamChunks(2, 80*time.Millisecond)}
	s := newGateTestService(t, engine)

	log := &frameLog{}
	noopEvents := conns.EventWriter(func(*conns.DataEvent) bool { return true })

	gate := make(chan struct{})
	done, err := s.playTTSStreaming(context.Background(), s.logger, noopEvents, log.writer(),
		"hello world hello world", uuid.New(), nil, NewProcessorState(), &db.UserSettings{}, gate)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(500 * time.Millisecond)
	if n := log.count(); n != 0 {
		t.Fatalf("emitted %d frames before gate opened", n)
	}

	close(gate)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("track did not finish after gate opened")
	}
	// 2 chunk frames + 1 track_done
	if n := log.count(); n != 3 {
		t.Fatalf("expected 3 frames after gate, got %d", n)
	}
}

func TestPlayTTSStreamingNilGate(t *testing.T) {
	engine := &fakeStreamEngine{chunks: streamChunks(2, 80*time.Millisecond)}
	s := newGateTestService(t, engine)

	log := &frameLog{}
	noopEvents := conns.EventWriter(func(*conns.DataEvent) bool { return true })

	done, err := s.playTTSStreaming(context.Background(), s.logger, noopEvents, log.writer(),
		"hello world hello world", uuid.New(), nil, NewProcessorState(), &db.UserSettings{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("track did not finish")
	}
	if n := log.count(); n != 3 {
		t.Fatalf("expected 3 frames, got %d", n)
	}
}

func TestPlayTTSStreamingSkipWhileGated(t *testing.T) {
	engine := &fakeStreamEngine{chunks: streamChunks(2, 80*time.Millisecond)}
	s := newGateTestService(t, engine)

	log := &frameLog{}
	noopEvents := conns.EventWriter(func(*conns.DataEvent) bool { return true })

	state := NewProcessorState()
	msgID := uuid.New()
	gate := make(chan struct{})

	done, err := s.playTTSStreaming(context.Background(), s.logger, noopEvents, log.writer(),
		"hello world hello world", msgID, nil, state, &db.UserSettings{}, gate)
	if err != nil {
		t.Fatal(err)
	}

	state.AddSkipped(msgID)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("gated track did not stop on skip")
	}
	if n := log.count(); n != 0 {
		t.Fatalf("skipped gated track emitted %d frames", n)
	}
}
