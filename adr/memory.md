# ADR: AI Character Memory

Status: proposed
Date: 2026-07-03

## Goal

Characters are stateless today: every AI redemption is generated from the static card plus the
single current message. No callbacks, no inside jokes, no lore accumulating across streams. The
entertainment value of a stream character is continuity (the Neuro-sama proof-of-market), so
statelessness caps the ceiling at novelty, which decays. Memory converts individual redemptions
into a serialized show the community co-authors, and gives viewers a reason to redeem again as
themselves.

Non-goal: chatbot-style per-conversation coherence. The audience for a reply is the stream, not
the requester; memory serves callback humor and lore, not dialogue state.

## Current state (verified 2026-07-03)

- `CharacterReply` receives only card + current message. Agentic history is in-memory per
  redemption and discarded. `msg_queue` is purged by `CleanQueue` past ~200 queue events.
  `chat_users` stores only a voice preference. `pkg/memory` is dead code (in-memory ring buffer,
  no persistence, no per-character keying) — ignore or delete it.
- llm_text3 (Qwen3.6-27B Q6_K, llama-server, port 3334): `--ctx-size 16384`, unified KV pool
  auto-split over 4 slots. Model `n_ctx_train = 262144`, so 16k is 1/16 of native capacity —
  raising ctx needs no RoPE/YaRN tricks.
- Measured prompt sizes (all 618 prompts in the service journal): p50 = 1,284 tokens,
  p99 = 2,342, max = 3,433. Worst-case card (Leon S. Kennedy, 16k-char description) assembled
  through `chatSystemPrompt` + max 500-char Twitch message = ~4,100 tokens exact via `/tokenize`.
- KV cost measured from logs: 64 KiB/token (+~150 MiB fixed SWA state per sequence). Each extra
  16k of ctx ≈ 1 GiB VRAM; GPU had ~9.6 GiB free at time of writing.

## Decision

A capped, curated list of text memories in postgres. No vector DB, no retrieval model, no
summarizer on the hot path. The prompt exposure is **constant-size by construction**: memory is
a whiteboard of at most ~20 lines (~200 chars each, ~600–1,000 tokens total), never a transcript.
The DB may hold more rows than the prompt shows; the injection query picks a subset. Database ≠
context — the model never sees the archive.

### Schema

```sql
create table char_memories (
    id uuid default uuid_generate_v7() primary key,

    user_id uuid references users(id),        -- null = global (travels with the public card)
    card_id uuid references char_cards(id),   -- null = all of the streamer's characters
    -- both null: disallowed (no platform-wide memories)

    subject_twitch_user_id bigint,            -- null = not about a specific viewer

    text text not null,                       -- distilled fact, second person, <= ~200 chars
    token_count int not null,                 -- exact, via llama-server /tokenize at write time

    pinned boolean not null default false,
    expires_at timestamp,                     -- null = permanent lore; TTL presets in UI
    suggested boolean not null default false, -- tier B: awaiting mod approval, never injected

    created_by uuid references users(id),
    created_at timestamp not null default now()
);
```

Scopes:

| user_id | card_id | meaning                          | CRUD access            |
|---------|---------|----------------------------------|------------------------|
| set     | set     | streamer's memory, one character | streamer + their mods  |
| set     | null    | streamer-wide, all characters    | streamer + their mods  |
| null    | set     | global character memory          | Admin role (+ card owner) |

- `UserSettings.DisableGlobalMemories` — streamer opt-out of the global scope.
- `chat_users.memory_opt_out` — set by `^^optout` / cleared by `^^optin` in chat (ingest path).
  On opt-out: delete rows tagged with that `subject_twitch_user_id`, reject future tagged
  inserts. Honest scope: this governs *tagged* memories and future auto-capture; it cannot
  police free-text prose that merely mentions a viewer.

### Injection (read path — zero LLM calls)

Plain SQL: active (`not suggested`, not expired), global scope unless opted out, ordered
pinned → newest, plus a small random rotation from the remainder, `LIMIT ~20`, hard token-sum
cap. Rendered as a `Things you remember:` block inside `chatSystemPrompt`, **after**
personality, **before** the few-shot `MessageExamples` (examples stay the final style anchor).
Same block for `DialogueReply` so agentic scenes inherit lore.

**`CompletionClient` (lexi / raw completions) is untouched.** The prompt format there is
load-bearing (hard constraint); the completions path simply ignores memory.

Keep the block position in the message array after the static prefix so llama.cpp prompt
caching keeps the stable part warm; rotation only invalidates the tail.

### Write path — three tiers of automation

- **Tier A (v1): humans only.** CRUD page (streamer/mod vs admin per existing role groups) +
  a one-click "remember this" button on processed messages in the control panel (mods already
  stare at request + `AIResponse` there). Prefill = the exchange, editable, default TTL 24h.
  This button is the difference between a feature that accumulates lore and a table that stays
  empty.
- **Tier B: deepseek distills, human approves.** The existing `oai` client
  (`deepseek-v4-flash`, already used for llmfilter) runs off the hot path (nightly or on
  stream end) over the day's interactions with a strict prompt: *"extract at most ONE
  memorable fact, <= 200 chars, second person, or output NONE."* NONE is the expected answer
  for ~95% of interactions — say so in the prompt or it will invent significance. Output lands
  as `suggested = true`, shown as a pending list in the control panel, one-click approve.
- **Tier C: fully automatic.** Same extraction, no approval. Only if tier B approvals become
  rubber-stamping.

Tier A ships first; B and C write into the same table, so nothing is throwaway.

Interaction log for tier B: a `char_interactions` append-only table (broadcaster, card,
twitch_user_id, requester_login, filtered request/reply, created_at). Written by
`handler_ai.go` **only after playback completes unskipped** — a skip is a mod's content
judgment; memory must not become a persistence mechanism for skipped content. Store the
*filtered* reply so censored spans can't re-enter prompts.

### Token counting

`POST localhost:3334/tokenize` — the engine's own tokenizer, exact, zero deps, stays correct
if the model on the port changes. Used at CRUD write time (store `token_count`, enforce the
per-memory and per-block budgets in tokens). chars/4 is fine as a live UI estimate only. No Go
tokenizer libraries (tiktoken vocab is wrong for Qwen; loading HF tokenizer.json is heavy).

### Context window

No change needed for typical traffic (p50 1.3k + memory block ≈ 2.3k). But the measured tail
stack-up — Leon-sized card (4.1k) + memory (1k) + one image (`--image-max-tokens 4096`) ≈
~9.6k — means two such concurrent requests contend for the unified 16k pool and queue. When
memory ships, bump `--ctx-size 16384 → 32768` in `/home/forsen/repos/llm/run_text_qwen.sh`
(measured cost ≈ +1 GiB VRAM) and restart `llm_text3`. This is un-capping, not extension:
32k is 12% of `n_ctx_train`; no context-rot concern at these sizes. Monitor via
`prompt_save` lines in the journal.

## Rejected alternatives

- **pgvector / embeddings / RAG.** Wrong relevance function for entertainment: cosine
  similarity surfaces the most on-topic memory, which is the least surprising one — the
  funniest callback is usually unrelated to the current message. Retrieval exists to choose a
  subset when you can't afford the whole set; a curated 20 fits entirely, every request. Also:
  another model service on an already-loaded GPU, another hot-path hop. Escalation ladder if a
  cap is ever truly outgrown: injection policy (pinned + newest + rotation) → exact-key
  per-viewer lookup (`subject_twitch_user_id = requester`) → pg_trgm/FTS lexical match →
  pgvector last. Expectation: the last rung never gets built.
- **Automatic summarizer as v1.** Poisoning, style drift, and summary quality are the three
  hardest problems; human-gated writes sidestep all three on day one.
- **Reusing `msg_queue` / `history` table / `pkg/memory`.** Purged / dead audit-log schema /
  in-memory-only respectively.

## Pitfalls

1. **Memory poisoning is the first thing chat will attempt.** Defense is the write gate
   (human approval), skip-aware logging, and cheap deletion — curation beats prevention, and
   some of what chat produces will be the *good* lore.
2. **Style drift / self-imitation.** Never feed raw transcripts back; only distilled
   second-person facts, capped, positioned before the few-shot examples. Watch for the
   character referencing memories too eagerly — if every reply opens with a callback, shrink
   the block or reword the header.
3. **Curation fatigue is the real bottleneck, not context.** The list hits 20 and nobody
   prunes, so new lore can't get in. TTL fights it; UI must show slot usage ("18/20") and sort
   by oldest-unpinned. If the table stays empty instead, the "remember this" button was
   skipped — build it.
4. **Unbounded card sizes.** Card text is unbounded user input (Leon: 16k chars). The memory
   block must remain the capped component; consider a card-size warning in the editor
   separately.
5. **Prompt-cache invalidation.** Random rotation changes the prompt every request past the
   memory block. If prefill latency shows, drop rotation before dropping memory count —
   pinned + newest is deterministic between writes.
6. **Blocklist drift.** A memory written before a word was blocklisted can smuggle it back
   into TTS. Run memory text through the regex filter at *inject* time, not only at write time.
7. **Global memory trust boundary.** Per-streamer mods must never write the global scope —
   global rows render on every stream using that card. Admin role (and possibly card owner)
   only.
8. **Opt-out honesty.** `^^optout` covers tagged memories and auto-capture. Do not present it
   as covering free-text mentions; no system can police prose.
9. **TTL semantics on stream boundaries.** "24h" is a proxy for "this stream". If streams run
   long or back-to-back, session memories bleed. Acceptable for v1; a "wipe session memories"
   button (delete all TTL'd rows) is the manual override, analogous to `clean_overlay`.

## Rollout

1. Migration (`char_memories`, `char_interactions`, `chat_users.memory_opt_out`,
   `UserSettings.DisableGlobalMemories`) + db functions + injection in `ChatClient` /
   `DialogueReply` + hard caps + `/tokenize` client method.
2. CRUD pages + control-panel "remember this" + wipe-session button + `^^optout`/`^^optin`.
3. `--ctx-size 32768` bump on llm_text3.
4. Observe a few streams → tier B (deepseek suggestions) → maybe tier C.
