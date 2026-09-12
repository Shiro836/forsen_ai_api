# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

BAJ AI: Twitch viewers redeem channel-point rewards; their messages are queued, run through LLM + TTS, and played on the streamer's OBS browser source. Moderators manage the queue from a web control panel (skip, show/hide images). Everything user-facing is server-rendered Go templates + htmx + vanilla JS.

## Commands

- **Build (production binaries):** `scripts/build_prod.sh` — builds `prod` (main server), `twitch-ingest`, and `clanker` at the repo root. Always use this script for deployable binaries, not raw `go build`. `go build ./...` is fine as a typecheck.
- **Test:** `go test ./...`. Single test: `go test ./internal/app/processor/ -run TestName`. Some tests self-skip when `ffmpeg`/`ffprobe` aren't in PATH.
- **Integration tests** are behind the `integration` build tag and hit live local services (e.g. the LLM on `:3334`): `go test -tags integration ./pkg/llm/ -run Live -v`. Don't run them casually; they need the model servers up. Any new test that talks to a live service gets the tag — `go test ./...` must pass on a machine with nothing running.
- **Container tests** (same `integration` tag) spin up throwaway stores with testcontainers instead of touching live ones: `go test -tags integration ./internal/app/history/ -run Containers -v` starts `pg_uuidv7:latest` (the local postgres build, applies `db/migrations`) and the pinned ClickHouse image (applies `db/ch_migrations`), then drives the real write path, the exporter and the history reader end to end (~20 s). Prefer this shape over live-DB tests for anything that writes.
- **LLM filter corpus:** `pkg/llmfilter/corpus_*_test.go` (~380 cases, themed: recall / precision / context / streamer rules / robustness / figures) runs against `filter_llm` (the local production model) by default; `FILTER_PROVIDER=oai` targets DeepSeek, `oai_candidate` (or `FILTER_CANDIDATE=1`) a candidate; subtests run 4-wide (`FILTER_PARALLEL`). Measured 2026-09-12: a full corpus run is 11-16 min on the local model, 3.5-4.6 min on DeepSeek V4.1-Flash via DeepInfra. Add a case for every filter bug you fix; the harness prints a `MASKSTATS` line for provider comparison.
- **Filter prompt changes:** optimize for the local model. Baseline the full corpus on it *before* editing (results drift between runs, so a single post-edit run proves nothing), then iterate with a `-run` regex naming ONLY the cases the change is about, run twice, then the full corpus and diff the failing-case lists against baseline. Keep that set to a handful of cases and leave regression coverage to the full corpus: measured 2026-09-12, the local model generates ~10 tok/s per slot and the echo protocol makes it retype the whole message, so a case costs ~4 s of server time and 3-5x that once the 4 slots are shared with the live stream — the `TestFigures|reply_chants` set this line used to prescribe is 33 cases and ~6.4 min per pass. The local model is prompt-fragile: any few-shot example or allowlist clause that shows a proper noun as a clean mention flips the elicited-lookalike reply case; state rules inside a bullet, no clean-mention examples. Real chat: `ARCHIVE_REPLAY=250 go test -tags integration ./pkg/llmfilter/ -run TestReplayArchive -v` re-judges recent archived requests from ClickHouse and prints span diffs (built-in policy only, no streamer rules or repeat collapse — read the diff with that in mind).
- **Config:** `cfg/cfg.yaml` (binaries take `-cfg-path`). Monitoring stack (prometheus/loki/grafana) runs via `docker-compose.yml`.
- **DB migrations:** numbered SQL files in `db/migrations/`; schema changes get a new file, never edits to applied ones. `scripts/backup_db.sh` dumps postgres using the conn string from cfg.

## Architecture

Two long-running services: `cmd/twitch-ingest` (joins Twitch chat/EventSub, pushes messages into postgres) and `cmd/app` (everything else). They share the DB as the queue.

**Message pipeline.** Messages live in postgres with a status state machine (`db.MsgStatus*`: wait → current → processed/deleted). `conns.Manager.HandleUser` runs one processor goroutine per streamer; `processor.Process` polls `GetNextMsg`, marks it current, and routes by reward type to one of five handlers in `internal/app/processor/`: AI (character reply), TTS, Universal TTS (voice/filter/sfx tags, rendered via `pkg/audiotree`; a `{sing[:melody]}` filter sends the span to the singing service on `:5114` via `pkg/ai.SingerClient`, non-streaming only — `go test -tags integration ./internal/app/processor/ -run TestUniversalSingLive -v` drives the real path), Agentic (multi-character dialogue), Chat TTS (plain chat messages).

**Event fan-out.** `conns.Manager` wraps an in-memory watermill pubsub with three topic families per user:
- `user.events.<id>` — data events (audio/text/image/skip) consumed by the OBS overlay websocket (`internal/app/api/obs_overlay.go`)
- `user.control.<id>` — control signals (skip message, show images, restart) consumed by the processor; bridged into a buffered channel in `HandleUser`
- `controlpanel.<id>` — a poke telling control-panel websockets to re-query `GetMessageUpdates`

Subscriber channels are **drop-on-full by design** — never assume guaranteed delivery of a single event; state that matters must live in the DB or `ProcessorState`.

**Skip protocol (subtle, has burned us repeatedly).** Text/audio events carry `msg_id`. A skip event is JSON `{msg_id, current}`: the client *always* records the id in `pending_skips` (to drop in-flight events) but only wipes the screen when `current` is true — skipping a queued message must not clear what's playing. Server-side, `ProcessorState.skippedMsgIDs` is the source of truth: every handler checks `IsSkipped` between pipeline stages, and `playTTS`'s ticker enforces it mid-playback. If you add a handler or a new stage, add the check.

**TTS/audio.** `pkg/ai` — IndexTTS (main engine) and StyleTTS2 (the `{old}` filter / chat voice). Word-level timings come from the engine or `pkg/whisperx`; on-screen text is revealed as cumulative prefixes timed against the audio (`timingTextPrefixes`). `pkg/ffmpeg` applies audio filters; `pkg/audiotree` folds per-segment filter stacks into a span tree so filters cover whole concatenated spans.

**Filtering.** `pkg/textfilter` is the shared span vocabulary; regex spans (per-user word lists) and `pkg/llmfilter` (LLM-judged hateful spans, via `pkg/oai`) are merged, then censored for TTS and highlighted in the control panel. `pkg/artfilter` detects character art (braille walls, box-drawing, symbol spam) by Unicode range — an LLM cannot see 2D shapes in a token stream, so this is deterministic. `pkg/llmfilter/phonetic.go` is deterministic for the same reason: a slur living only in the gap between two innocent words ("gain eagers") never appears in the token stream, so espeak-ng voices the text, the word gaps are removed and the sound is matched against slur pronunciations; a hit adds a SPOKEN FORM block and the model still judges. Needs `espeak-ng` and `/usr/share/cracklib/cracklib-small` on the host — without either it degrades with a logged warning. Art is collapsed out of the llmfilter's input (a braille wall is ~10k tokens and stalls the echo protocol) and masked as `(ascii art)` on every display surface in `playTTS`/`playTTSStreaming`, but deliberately still reaches TTS: the engine mumbling through a wall is a feature, showing it on stream is not.

**Storage.** Raw SQL in `db/` (no ORM), pgx. Media (images, voice references) lives in minio/S3 — `db.AttachS3Client` makes card/image reads transparently fetch from S3.

## Hard constraints

- **Never modify the `Ask()` / raw completions path used for Llama roleplay** (`pkg/llm`, `CompletionClient`). The prompt format is load-bearing for roleplay quality.
- **Never suggest FP16 for Index-TTS** — it audibly ruins output.
- OBS browser sources cache static JS aggressively; changes to `internal/app/api/static/*.js` need a browser-source cache refresh to take effect.
- Don't estimate VRAM or model sizes — measure on the actual machine.

## Before starting any task

Read these memory files from the auto-memory directory (`~/.claude/projects/-home-forsen-repos-forsen-ai-api/memory/`) and follow them; MEMORY.md alone is not enough:

- `feedback_use_build_script.md` — every change ends with `scripts/build_prod.sh` before reporting done. The user restarts the service after you finish; `go build ./...` only typechecks and leaves the old binary running.
- `feedback_sudo_commands.md`, `feedback_db_readonly.md`, `feedback_secrets_public_repo.md` — what you may not execute or write.
- `feedback_verify_live_state_first.md` — verify the live stack before asserting anything about it.
- Any `project_*.md` that the index lists for the area you're touching.

## How to work

- **Validate before you code.** Everything runs on this machine — the services, their logs (`journalctl`), the DB, the model servers. When behavior is in question, probe the live service or read the logs instead of assuming; design from measured facts. (Example: "does the engine's segment text match the input?" is one curl away — don't guess and code defensively around it.)
- **Fallbacks only when absolutely necessary.** A fallback that silently degrades output hides bugs; prefer failing loudly with a logged error. Add one only when the degraded result is genuinely better than no result (e.g. karaoke interpolation when the aligner is down), it must always log, and never plant one to paper over a case you didn't understand. When unsure whether to fail or degrade, ask.
- **Decisions with tradeoffs get a decision tree.** When several approaches exist, lay out the options with concrete pros/cons (measured, not imagined). If one dominates, take it and say why; if it's a real tradeoff — especially anything touching audio quality or on-stream visuals — present the tree and let the user pick. Never silently change what the TTS sounds like or how the overlay looks to win latency or simplicity.

## Code style

- **Comments: only non-obvious WHY** — a constraint, a gotcha, why a value was chosen. Never restate what the code says, never narrate the current task or bugfix ("fixes the re-type bug") — comments must make sense to a cold reader years later; task context belongs in the commit message. If it's easy to read from the code, don't comment it. No commented-out code; git remembers.
- Exported identifiers and packages get real doc comments in the existing style, but short — 2-3 lines stating the contract, not an essay on mechanics the code already shows. `pkg/audiotree` and `pkg/llmfilter` are the exemplars.
- **Before writing any comment, apply this test:** would someone who never saw this task, reading the code cold in 6-7 months, still need this sentence? If no, delete it. Default to no comment. These keep getting written and are always wrong:
  - Restating params or behavior a reader gets from the signature and body ("`checkSpans` scores one case; flagged lists substrings that must overlap a span").
  - Justifying a change to the code rather than describing the code ("no separate nonEmpty knob is needed", "the two passes merge rather than replace").
  - A second sentence that merely follows from the first, or from the code.
  - Restating the name of the thing it sits on (a `// a respelled slur is still a slur` over a case named `n-word respelling is flagged`).
- Errors: wrap with `fmt.Errorf("context: %w", err)`. The processor loop logs and continues on per-message failures; don't let one bad message kill a streamer's processor.
- Logging: `slog` with structured attrs; derive contextual loggers via `logger.With("user", ...)` / `WithGroup`.
- Frontend JS is dependency-free vanilla; keep it that way. Websocket payloads are JSON with base64 `data`; new event fields must stay backward-compatible with cached overlay JS (parse defensively, default to old behavior).
