# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

BAJ AI: Twitch viewers redeem channel-point rewards; their messages are queued, run through LLM + TTS, and played on the streamer's OBS browser source. Moderators manage the queue from a web control panel (skip, show/hide images). Everything user-facing is server-rendered Go templates + htmx + vanilla JS.

## Commands

- **Build (production binaries):** `scripts/build_prod.sh` — builds `prod` (main server), `twitch-ingest`, and `clanker` at the repo root. Always use this script for deployable binaries, not raw `go build`. `go build ./...` is fine as a typecheck.
- **Test:** `go test ./...`. Single test: `go test ./internal/app/processor/ -run TestName`. Some tests self-skip when `ffmpeg`/`ffprobe` aren't in PATH.
- **Integration tests** are behind the `integration` build tag and hit live local services (e.g. the LLM on `:3334`): `go test -tags integration ./pkg/llm/ -run Live -v`. Don't run them casually; they need the model servers up.
- **Config:** `cfg/cfg.yaml` (binaries take `-cfg-path`). Monitoring stack (prometheus/loki/grafana) runs via `docker-compose.yml`.
- **DB migrations:** numbered SQL files in `db/migrations/`; schema changes get a new file, never edits to applied ones. `scripts/backup_db.sh` dumps postgres using the conn string from cfg.

## Architecture

Two long-running services: `cmd/twitch-ingest` (joins Twitch chat/EventSub, pushes messages into postgres) and `cmd/app` (everything else). They share the DB as the queue.

**Message pipeline.** Messages live in postgres with a status state machine (`db.MsgStatus*`: wait → current → processed/deleted). `conns.Manager.HandleUser` runs one processor goroutine per streamer; `processor.Process` polls `GetNextMsg`, marks it current, and routes by reward type to one of five handlers in `internal/app/processor/`: AI (character reply), TTS, Universal TTS (voice/filter/sfx tags, rendered via `pkg/audiotree`), Agentic (multi-character dialogue), Chat TTS (plain chat messages).

**Event fan-out.** `conns.Manager` wraps an in-memory watermill pubsub with three topic families per user:
- `user.events.<id>` — data events (audio/text/image/skip) consumed by the OBS overlay websocket (`internal/app/api/obs_overlay.go`)
- `user.control.<id>` — control signals (skip message, show images, restart) consumed by the processor; bridged into a buffered channel in `HandleUser`
- `controlpanel.<id>` — a poke telling control-panel websockets to re-query `GetMessageUpdates`

Subscriber channels are **drop-on-full by design** — never assume guaranteed delivery of a single event; state that matters must live in the DB or `ProcessorState`.

**Skip protocol (subtle, has burned us repeatedly).** Text/audio events carry `msg_id`. A skip event is JSON `{msg_id, current}`: the client *always* records the id in `pending_skips` (to drop in-flight events) but only wipes the screen when `current` is true — skipping a queued message must not clear what's playing. Server-side, `ProcessorState.skippedMsgIDs` is the source of truth: every handler checks `IsSkipped` between pipeline stages, and `playTTS`'s ticker enforces it mid-playback. If you add a handler or a new stage, add the check.

**TTS/audio.** `pkg/ai` — IndexTTS (main engine) and StyleTTS2 (the `{old}` filter / chat voice). Word-level timings come from the engine or `pkg/whisperx`; on-screen text is revealed as cumulative prefixes timed against the audio (`timingTextPrefixes`). `pkg/ffmpeg` applies audio filters; `pkg/audiotree` folds per-segment filter stacks into a span tree so filters cover whole concatenated spans.

**Filtering.** `pkg/textfilter` is the shared span vocabulary; regex spans (per-user word lists) and `pkg/llmfilter` (LLM-judged hateful spans, via `pkg/oai`) are merged, then censored for TTS and highlighted in the control panel.

**Storage.** Raw SQL in `db/` (no ORM), pgx. Media (images, voice references) lives in minio/S3 — `db.AttachS3Client` makes card/image reads transparently fetch from S3.

## Hard constraints

- **Never modify the `Ask()` / raw completions path used for Llama roleplay** (`pkg/llm`, `CompletionClient`). The prompt format is load-bearing for roleplay quality.
- **Never suggest FP16 for Index-TTS** — it audibly ruins output.
- OBS browser sources cache static JS aggressively; changes to `internal/app/api/static/*.js` need a browser-source cache refresh to take effect.
- Don't estimate VRAM or model sizes — measure on the actual machine.

## Code style

- **Comments: only non-obvious WHY** — a constraint, a gotcha, why a value was chosen. Never restate what the code says, never narrate the current task or bugfix ("fixes the re-type bug") — comments must make sense to a cold reader years later; task context belongs in the commit message. No commented-out code; git remembers.
- Exported identifiers and packages get real doc comments in the existing style — `pkg/audiotree` and `pkg/llmfilter` are the exemplars.
- Errors: wrap with `fmt.Errorf("context: %w", err)`. The processor loop logs and continues on per-message failures; don't let one bad message kill a streamer's processor.
- Logging: `slog` with structured attrs; derive contextual loggers via `logger.With("user", ...)` / `WithGroup`.
- Frontend JS is dependency-free vanilla; keep it that way. Websocket payloads are JSON with base64 `data`; new event fields must stay backward-compatible with cached overlay JS (parse defensively, default to old behavior).
