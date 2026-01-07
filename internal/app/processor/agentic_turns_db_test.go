package processor

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"app/db"
	"app/internal/app/conns"
	"app/pkg/agentic"
	"app/pkg/ffmpeg"
	"app/pkg/llm"
	"app/pkg/whisperx"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

var agenticTurnsCfgPath string
var agenticTurnsOnly bool
var agenticColorNames bool

func init() {
	// Match cmd/app/main.go behavior (flag name + default).
	flag.StringVar(&agenticTurnsCfgPath, "cfg-path", "cfg/cfg.yaml", "path to config file")
	flag.BoolVar(&agenticTurnsOnly, "agentic-turns-only", true, "print only character turns (no dialoguePrompt context blocks)")
	flag.BoolVar(&agenticColorNames, "agentic-color-names", true, "colorize character names in console output.")
}

func TestAgenticTurns_DBIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfgBytes, cfgResolvedPath := mustReadCfg(t, agenticTurnsCfgPath)

	// Avoid importing app/cfg here (it imports api -> processor, causing an import cycle in this package test).
	// We only need DB config anyway.
	var c struct {
		DB db.Config `yaml:"db"`
	}
	require.NoError(t, yaml.Unmarshal(cfgBytes, &c), "failed to unmarshal cfg from %s", cfgResolvedPath)

	database, err := db.New(ctx, &c.DB)
	require.NoError(t, err)

	// IDs provided by user request (must be used as-is).
	idA := uuid.MustParse("0190cb20-54a4-7925-ad5b-a8e76b6f3ad9")
	idB := uuid.MustParse("019409a8-9a8e-755a-b7cd-6120eea0bc2b")

	cardA, err := database.GetCharCardByID(ctx, uuid.Nil, idA)
	require.NoError(t, err)
	require.NotNil(t, cardA)

	cardB, err := database.GetCharCardByID(ctx, uuid.Nil, idB)
	require.NoError(t, err)
	require.NotNil(t, cardB)

	// This mirrors the agentic flow’s history building:
	// - `appendHistoryTurn` writes "Name: text" into `llm.Message{Role:"user"}`
	// - `collectHistoryTurns` returns the string turns used by `dialoguePrompt(...)`
	history := []llm.Message{}
	appendHistoryTurn(&history, cardA.Name, "hello")
	appendHistoryTurn(&history, cardB.Name, "hello back")

	turns := collectHistoryTurns(history)
	require.Len(t, turns, 2)
	require.True(t, strings.HasPrefix(turns[0], cardA.Name+": "), "unexpected turn[0]=%q", turns[0])
	require.True(t, strings.HasPrefix(turns[1], cardB.Name+": "), "unexpected turn[1]=%q", turns[1])

	// Print turns to console (run with -v to always see output). Avoid t.Log (it prefixes file:line).
	for i, turn := range turns {
		_ = i
		fmt.Println(turn)
	}
}

// This test runs the real AgenticHandler.Handle flow end-to-end, but uses:
// - Real DB for character cards (the two IDs above)
// - Local stub HTTP server for detector/planner (guided chat completions)
// - Local stub LLM for dialogue generation (legacy Ask)
// - Local stub TTS that generates a tiny WAV (so ffmpeg/ffprobe can process it)
//
// Output: prints speaker turns to console.
func TestAgenticHandle_DBIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfgBytes, cfgResolvedPath := mustReadCfg(t, agenticTurnsCfgPath)

	var c struct {
		DB     db.Config `yaml:"db"`
		Ffmpeg struct {
			TmpDir string `yaml:"tmp_dir"`
		} `yaml:"ffmpeg"`
	}
	require.NoError(t, yaml.Unmarshal(cfgBytes, &c), "failed to unmarshal cfg from %s", cfgResolvedPath)

	database, err := db.New(ctx, &c.DB)
	require.NoError(t, err)

	// IDs provided by user request (must be used as-is).
	idA := uuid.MustParse("0190cb20-54a4-7925-ad5b-a8e76b6f3ad9")
	idB := uuid.MustParse("019409a8-9a8e-755a-b7cd-6120eea0bc2b")

	cardA, err := database.GetCharCardByID(ctx, uuid.Nil, idA)
	require.NoError(t, err)
	cardB, err := database.GetCharCardByID(ctx, uuid.Nil, idB)
	require.NoError(t, err)

	// Map ID -> Name for printing.
	idToName := map[uuid.UUID]string{
		cardA.ID: cardA.Name,
		cardB.ID: cardB.Name,
	}

	// Stub guided LLM (used by agentic.Detector + agentic.Planner).
	stubServer := newAgenticGuidedStubServer(t, cardA.Name, cardB.Name)
	defer stubServer.Close()

	httpClient := stubServer.Client()
	agenticLLM := llm.New(httpClient, &llm.Config{
		URL:       stubServer.URL,
		Model:     "stub",
		MaxTokens: 256,
		MinTokens: 1,
	})
	detector := agentic.NewDetector(agenticLLM)
	planner := agentic.NewPlanner(agenticLLM)

	// Stub dialogue LLM used inside prepareNextAgenticTurn().
	dialogueLLM := &stubDialogueLLM{
		bySpeaker: map[string]string{
			cardB.Name: "okay, let's go.",
			cardA.Name: "sure.",
		},
	}

	// Service dependencies (ffmpeg + tts).
	tmpDir := strings.TrimSpace(c.Ffmpeg.TmpDir)
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}
	ff := ffmpeg.New(&ffmpeg.Config{TmpDir: tmpDir})

	// Ensure ffmpeg and ffprobe exist; skip if not present.
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skipf("ffmpeg not found in PATH: %v", err)
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skipf("ffprobe not found in PATH: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	svc := NewService(logger, database, nil, ff, &stubTTSEngine{}, nil, nil, nil, nil)
	h := NewAgenticHandler(logger, database, detector, planner, dialogueLLM, svc)

	msgID := uuid.New().String()
	state := NewProcessorState()

	// Print only "Speaker: text" (speaker inferred from image event).
	var currentSpeaker string
	imageRe := regexp.MustCompile(`/characters/([0-9a-fA-F-]+)/image`)

	eventWriter := func(ev *conns.DataEvent) bool {
		switch ev.EventType {
		case conns.EventTypeImage:
			m := imageRe.FindSubmatch(ev.EventData)
			if len(m) == 2 {
				if id, err := uuid.Parse(string(m[1])); err == nil {
					if name, ok := idToName[id]; ok {
						currentSpeaker = name
					} else {
						currentSpeaker = id.String()
					}
				}
			}
		case conns.EventTypeText:
			line := fmt.Sprintf("%s: %s", colorName(currentSpeaker), string(ev.EventData))
			fmt.Println(line)
		}
		return true
	}

	// Message is irrelevant for the stubbed detector/planner, but keep it realistic.
	input := InteractionInput{
		Requester:    "tester",
		Broadcaster:  &db.User{TwitchLogin: "tester"},
		Message:      fmt.Sprintf("%s and %s talk about microphones", cardA.Name, cardB.Name),
		UserSettings: &db.UserSettings{},
		MsgID:        msgID,
		State:        state,
	}

	require.NoError(t, h.Handle(ctx, input, eventWriter))
}

func TestAgenticHandle_RealLLM_DBIntegration_PrintDialoguePrompts(t *testing.T) {
	// Uses real DB + real agentic LLM endpoints from cfg.yaml.
	// Only TTS is stubbed (fake audio), everything else (agentic + db + llm) is real.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cfgBytes, cfgResolvedPath := mustReadCfg(t, agenticTurnsCfgPath)

	var c struct {
		DB         db.Config  `yaml:"db"`
		AgenticLLM llm.Config `yaml:"agentic_llm"`
		Ffmpeg     struct {
			TmpDir string `yaml:"tmp_dir"`
		} `yaml:"ffmpeg"`
	}
	require.NoError(t, yaml.Unmarshal(cfgBytes, &c), "failed to unmarshal cfg from %s", cfgResolvedPath)

	database, err := db.New(ctx, &c.DB)
	require.NoError(t, err)

	// IDs provided by user request (must be used as-is).
	idA := uuid.MustParse("0190cb20-54a4-7925-ad5b-a8e76b6f3ad9")
	idB := uuid.MustParse("019409a8-9a8e-755a-b7cd-6120eea0bc2b")

	cardA, err := database.GetCharCardByID(ctx, uuid.Nil, idA)
	require.NoError(t, err)
	cardB, err := database.GetCharCardByID(ctx, uuid.Nil, idB)
	require.NoError(t, err)

	idToName := map[uuid.UUID]string{
		cardA.ID: cardA.Name,
		cardB.ID: cardB.Name,
	}

	httpClient := &http.Client{Timeout: 90 * time.Second}
	agenticLLM := llm.New(httpClient, &c.AgenticLLM)
	detector := agentic.NewDetector(agenticLLM)
	planner := agentic.NewPlanner(agenticLLM)

	// Wrap the real LLM to print exactly what dialoguePrompt produced (the prompt passed to Ask).
	dialogueLLM := &printingDialogueLLM{
		inner:      agenticLLM,
		maxPrompts: 10, // print after 0th..3rd dialoguePrompt call, then stop
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// We return fake audio, so playTTS still needs ffmpeg/ffprobe to compute duration and loop timing.
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skipf("ffmpeg not found in PATH: %v", err)
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skipf("ffprobe not found in PATH: %v", err)
	}

	tmpDir := strings.TrimSpace(c.Ffmpeg.TmpDir)
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}
	ff := ffmpeg.New(&ffmpeg.Config{TmpDir: tmpDir})

	// Use the existing ref wav, but trim it short so tests run fast.
	refPath := "/home/forsen/repos/forsen_ai_api/pkg/ai/refs/okayeg_ref.wav"
	refWav, err := os.ReadFile(refPath)
	require.NoError(t, err, "failed to read ref wav: %s", refPath)
	shortWav, err := ff.TrimToWav(ctx, refWav, 250*time.Millisecond)
	require.NoError(t, err)

	svc := NewService(logger, database, nil, ff, &fixedAudioTTSEngine{audio: shortWav}, nil, nil, nil, nil)

	h := NewAgenticHandler(logger, database, detector, planner, dialogueLLM, svc)

	var currentSpeaker string
	imageRe := regexp.MustCompile(`/characters/([0-9a-fA-F-]+)/image`)
	eventWriter := func(ev *conns.DataEvent) bool {
		switch ev.EventType {
		case conns.EventTypeImage:
			m := imageRe.FindSubmatch(ev.EventData)
			if len(m) == 2 {
				if id, err := uuid.Parse(string(m[1])); err == nil {
					if name, ok := idToName[id]; ok {
						currentSpeaker = name
					} else {
						currentSpeaker = id.String()
					}
				}
			}
		case conns.EventTypeText:
			if agenticTurnsOnly {
				fmt.Printf("%s: %s\n", colorName(currentSpeaker), string(ev.EventData))
			} else {
				fmt.Printf("TURN %s: %s\n", colorName(currentSpeaker), string(ev.EventData))
			}
		case conns.EventTypeAudio:
			// ignored (stubbed)
		}
		return true
	}

	msgID := uuid.New().String()
	input := InteractionInput{
		Requester:    "tester",
		Broadcaster:  &db.User{TwitchLogin: "tester"},
		Message:      fmt.Sprintf("%s and %s talk about microphones", cardA.Name, cardB.Name),
		UserSettings: &db.UserSettings{},
		MsgID:        msgID,
		State:        NewProcessorState(),
	}

	// Handle returns nil even when the wrapper stops generation after prompt #3.
	require.NoError(t, h.Handle(ctx, input, eventWriter))
}

// --- stubs / helpers ---

type stubDialogueLLM struct {
	bySpeaker map[string]string
}

func (s *stubDialogueLLM) Ask(ctx context.Context, prompt string) (string, error) {
	// Extremely naive: choose response based on which character name appears in the prompt.
	for name, resp := range s.bySpeaker {
		if strings.Contains(prompt, "<START>"+name+":") || strings.Contains(prompt, "\n<START>"+name+":") {
			return resp, nil
		}
	}
	// default
	return "ok.", nil
}

func (s *stubDialogueLLM) AskMessages(ctx context.Context, messages []llm.Message, attachments []llm.Attachment) (string, error) {
	return "ok.", nil
}

type stubTTSEngine struct{}

func (s *stubTTSEngine) TTS(ctx context.Context, text string, voiceReference []byte) ([]byte, []whisperx.Timiing, error) {
	// Generate ~200ms of silence WAV so ffmpeg/ffprobe can process it.
	return tinySilenceWav(200*time.Millisecond, 44100, 2), nil, nil
}

type fixedAudioTTSEngine struct {
	audio []byte
}

func (s *fixedAudioTTSEngine) TTS(ctx context.Context, text string, voiceReference []byte) ([]byte, []whisperx.Timiing, error) {
	_ = ctx
	_ = text
	_ = voiceReference
	return append([]byte(nil), s.audio...), nil, nil
}

type printingDialogueLLM struct {
	inner      *llm.Client
	maxPrompts int64
	callIdx    int64
}

func (p *printingDialogueLLM) Ask(ctx context.Context, prompt string) (string, error) {
	idx := atomic.AddInt64(&p.callIdx, 1) - 1

	if !agenticTurnsOnly {
		fmt.Printf("\n===== dialoguePrompt #%d BEGIN =====\n%s\n===== dialoguePrompt #%d END =====\n\n", idx, prompt, idx)

		// Stop after printing prompts 0..maxPrompts-1 (inclusive), so the test doesn't run forever.
		if p.maxPrompts > 0 && idx >= p.maxPrompts-1 {
			return "", fmt.Errorf("stopping after printing dialoguePrompt #%d", idx)
		}
	}

	return p.inner.Ask(ctx, prompt)
}

func (p *printingDialogueLLM) AskMessages(ctx context.Context, messages []llm.Message, attachments []llm.Attachment) (string, error) {
	return p.inner.AskMessages(ctx, messages, attachments)
}

func tinySilenceWav(d time.Duration, sampleRate int, channels int) []byte {
	// PCM 16-bit little endian.
	bytesPerSample := 2
	numSamples := int(float64(sampleRate) * d.Seconds())
	blockAlign := channels * bytesPerSample
	byteRate := sampleRate * blockAlign
	dataSize := numSamples * blockAlign

	// 44-byte WAV header + data.
	out := make([]byte, 44+dataSize)

	// RIFF header
	copy(out[0:4], []byte("RIFF"))
	putU32LE(out[4:8], uint32(36+dataSize))
	copy(out[8:12], []byte("WAVE"))

	// fmt chunk
	copy(out[12:16], []byte("fmt "))
	putU32LE(out[16:20], 16)                 // PCM fmt chunk size
	putU16LE(out[20:22], 1)                  // audio format = PCM
	putU16LE(out[22:24], uint16(channels))   // channels
	putU32LE(out[24:28], uint32(sampleRate)) // sample rate
	putU32LE(out[28:32], uint32(byteRate))   // byte rate
	putU16LE(out[32:34], uint16(blockAlign)) // block align
	putU16LE(out[34:36], 16)                 // bits per sample

	// data chunk
	copy(out[36:40], []byte("data"))
	putU32LE(out[40:44], uint32(dataSize))

	// data already zeroed = silence
	return out
}

func putU16LE(dst []byte, v uint16) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
}

func putU32LE(dst []byte, v uint32) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v >> 16)
	dst[3] = byte(v >> 24)
}

func colorName(name string) string {
	if !agenticColorNames || name == "" {
		return name
	}

	// Deterministic color based on name. Keep it simple and readable.
	colors := []string{
		"\x1b[31m", // red
		"\x1b[32m", // green
		"\x1b[33m", // yellow
		"\x1b[34m", // blue
		"\x1b[35m", // magenta
		"\x1b[36m", // cyan
	}
	const reset = "\x1b[0m"

	// FNV-1a-ish tiny hash (no extra imports).
	var h uint32 = 2166136261
	for i := 0; i < len(name); i++ {
		h ^= uint32(name[i])
		h *= 16777619
	}
	c := colors[int(h)%len(colors)]
	return c + name + reset
}

func newAgenticGuidedStubServer(t *testing.T, nameA, nameB string) *httptest.Server {
	t.Helper()

	var nextSpeakerCalls int

	handler := http.NewServeMux()
	handler.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)

		var req llm.ChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if req.StructuredOutputs == nil {
			http.Error(w, "missing structured_outputs", http.StatusBadRequest)
			return
		}

		var schema map[string]any
		_ = json.Unmarshal(req.StructuredOutputs.JSON, &schema)
		props, _ := schema["properties"].(map[string]any)

		var content any

		switch {
		case props != nil && props["characters"] != nil:
			content = map[string]any{
				"characters": []string{nameA, nameB},
			}
		case props != nil && props["first_speaker_name"] != nil:
			content = map[string]any{
				"first_speaker_name": nameA,
				"first_message_text": "hello",
			}
		case props != nil && props["next_speaker_name"] != nil:
			nextSpeakerCalls++
			next := "END"
			if nextSpeakerCalls == 1 {
				next = nameB
			}
			content = map[string]any{
				"next_speaker_name": next,
			}
		default:
			content = map[string]any{}
		}

		contentBytes, _ := json.Marshal(content)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": string(contentBytes)}},
			},
		})
	})

	return httptest.NewServer(handler)
}

func mustReadCfg(t *testing.T, cfgPath string) ([]byte, string) {
	t.Helper()

	if filepath.IsAbs(cfgPath) {
		b, err := os.ReadFile(cfgPath)
		require.NoError(t, err)
		return b, cfgPath
	}

	if root, ok := findRepoRoot(); ok {
		p := filepath.Join(root, cfgPath)
		if b, err := os.ReadFile(p); err == nil {
			return b, p
		}
	}

	b, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	return b, cfgPath
}

func findRepoRoot() (string, bool) {
	wd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
