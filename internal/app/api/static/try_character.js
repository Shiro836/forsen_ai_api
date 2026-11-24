// Try page (character / agentic / universal TTS / universal AI) on the overlay-v2 protocol.
// Audio arrives as raw binary frames on the same websocket that carries the
// JSON envelope; the first byte tells them apart ('{' vs frame type 1/2/3).
// Playback and karaoke are the same OverlayPlayer the OBS overlay uses.

(function () {
    let ws = null;
    let audioContext = null;
    let player = null;
    let characterID = null;

    // Per-track audio kept for the download links: track_id -> { msgId, text, chunks }
    const trackBuffers = new Map();
    let downloadEntries = [];

    function cleanup() {
        if (ws) {
            ws.close();
            ws = null;
        }
        if (player) {
            player.destroy();
            player = null;
        }
        clearDownloads();
    }

    function base64ToArrayBuffer(base64) {
        const binaryString = window.atob(base64);
        const bytes = new Uint8Array(binaryString.length);
        for (let i = 0; i < binaryString.length; i++) {
            bytes[i] = binaryString.charCodeAt(i);
        }
        return bytes.buffer;
    }

    function ensurePlayer() {
        if (!audioContext) {
            audioContext = new (window.AudioContext || window.webkitAudioContext)();
        }
        if (audioContext.state === 'suspended') {
            audioContext.resume();
        }
        if (!player) {
            player = new OverlayPlayer({
                audioContext: audioContext,
                cardEl: document.getElementById('try_caption_card'),
                windowEl: document.getElementById('try_caption_window'),
                textEl: document.getElementById('try_caption_text'),
                onChunk: collectChunk,
                onTrackDone: finishTrack,
            });
        }
        return player;
    }

    function setImage(url) {
        const charImg = document.getElementById("char_image");
        if (!charImg) return;
        // on the try page empty image events are ignored to keep the character visible
        if (!url || url.trim().length <= 1) return;
        charImg.src = url;
        charImg.style.opacity = "1";
    }

    // Each chunk is a standalone MP3 with its own ID3v2 tag; a tag in the
    // middle of the joined file makes decoders fail at every sentence
    // boundary, so only the first chunk keeps its tag.
    function stripID3(bytes) {
        if (bytes.length < 10 || bytes[0] !== 0x49 || bytes[1] !== 0x44 || bytes[2] !== 0x33) {
            return bytes;
        }
        const size = ((bytes[6] & 0x7f) << 21) | ((bytes[7] & 0x7f) << 14) | ((bytes[8] & 0x7f) << 7) | (bytes[9] & 0x7f);
        const footer = (bytes[5] & 0x10) ? 10 : 0;
        return bytes.subarray(10 + size + footer);
    }

    function collectChunk(header, mp3) {
        if (!header || !header.track_id) return;
        let track = trackBuffers.get(header.track_id);
        if (!track) {
            track = { msgId: header.msg_id, text: "", chunks: [] };
            trackBuffers.set(header.track_id, track);
        }
        // decodeAudioData detaches the frame buffer the player decodes, so the
        // page keeps its own copy for the download
        track.chunks.push({ seq: header.seq || 0, bytes: new Uint8Array(mp3.slice(0)) });
        if (!track.text) track.text = header.text || "";
    }

    function finishTrack(header) {
        if (!header || !header.track_id) return;
        const track = trackBuffers.get(header.track_id);
        trackBuffers.delete(header.track_id);
        if (!track || track.chunks.length === 0) return;

        track.chunks.sort((a, b) => a.seq - b.seq);
        const parts = track.chunks.map((c, i) => (i === 0 ? c.bytes : stripID3(c.bytes)));
        addDownloadLink(new Blob(parts, { type: "audio/mpeg" }), track.text);
    }

    function dropTrackBuffers(msgId) {
        if (!msgId) return;
        for (const [trackId, track] of trackBuffers) {
            if (track.msgId === msgId) trackBuffers.delete(trackId);
        }
    }

    function downloadName(text) {
        const slug = (text || "")
            .replace(/[\\/:*?"<>|\u0000-\u001f]/g, " ")
            .replace(/\s+/g, " ")
            .trim()
            .slice(0, 60)
            .trim();
        return "baj-" + (slug || "audio") + ".mp3";
    }

    function addDownloadLink(blob, text) {
        const container = document.getElementById("try_downloads");
        if (!container) return;

        const row = document.createElement("div");
        row.className = "flex min-w-0 items-center gap-3";

        const label = document.createElement("span");
        label.className = "min-w-0 flex-1 truncate text-sm opacity-70";
        label.textContent = text || "Audio";
        row.appendChild(label);

        const url = URL.createObjectURL(blob);
        const link = document.createElement("a");
        link.className = "shrink-0 rounded border-2 border-slate-300 px-3 py-1 text-sm hover:border-slate-700 dark:border-slate-700 dark:hover:border-twitch-light";
        link.href = url;
        link.download = downloadName(text);
        link.textContent = "Download MP3";
        row.appendChild(link);

        container.appendChild(row);
        container.classList.remove("hidden");

        downloadEntries.push({ url, row });
        while (downloadEntries.length > 20) {
            const old = downloadEntries.shift();
            URL.revokeObjectURL(old.url);
            old.row.remove();
        }
    }

    function clearDownloads() {
        for (const entry of downloadEntries) {
            URL.revokeObjectURL(entry.url);
            entry.row.remove();
        }
        downloadEntries = [];
        trackBuffers.clear();
        const container = document.getElementById("try_downloads");
        if (container) container.classList.add("hidden");
    }

    function setButtonsEnabled(enabled) {
        for (const id of ["tts_button", "universal_tts_button", "universal_ai_button", "ai_button", "agentic_button"]) {
            const btn = document.getElementById(id);
            if (btn) btn.disabled = !enabled;
        }
        const stopBtn = document.getElementById("stop_button");
        if (stopBtn) stopBtn.disabled = false;
    }

    function stopCurrentAction() {
        if (player) {
            const active = player.activeMsgId();
            if (active) player.skip(active, true);
            player.clearCaption();
        }
        if (ws && ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({ action: "stop", text: "" }));
        }
        setButtonsEnabled(true);
    }

    function sendAction(action) {
        const input = document.getElementById("message_input");
        if (!input) return;

        const text = input.value.trim();
        if (!text) return;

        if (!ws || ws.readyState !== WebSocket.OPEN) {
            console.error('WebSocket not connected');
            return;
        }

        ensurePlayer();
        const llmToggle = document.getElementById("llm_filter_toggle");
        const regexToggle = document.getElementById("regex_filter_toggle");
        ws.send(JSON.stringify({
            action: action,
            text: text,
            llm_filter: llmToggle ? llmToggle.checked : false,
            regex_filter: regexToggle ? regexToggle.checked : true,
        }));
        input.focus();
    }

    function handleEnvelope(msg) {
        const dataStr = new TextDecoder('utf-8').decode(base64ToArrayBuffer(msg.data));

        switch (msg.type) {
            case 'track_meta': {
                try {
                    const meta = JSON.parse(dataStr);
                    ensurePlayer().registerMeta(meta.track_id, meta.msg_id);
                    let track = trackBuffers.get(meta.track_id);
                    if (!track) {
                        track = { msgId: meta.msg_id, text: "", chunks: [] };
                        trackBuffers.set(meta.track_id, track);
                    }
                    track.text = meta.text || track.text;
                } catch (e) { }
                break;
            }
            case 'skip': {
                let msgId = dataStr;
                try {
                    const payload = JSON.parse(dataStr);
                    if (payload && payload.msg_id) msgId = payload.msg_id;
                } catch (e) { }
                dropTrackBuffers(msgId);
                if (player) player.skip(msgId, true);
                setButtonsEnabled(true);
                break;
            }
            case 'clean':
                // end of interaction: keep the last caption on screen, just unlock
                setButtonsEnabled(true);
                break;
            case 'image':
                setImage(dataStr);
                break;
            case 'text':
                // plain error text from the server (handler failures)
                if (dataStr && dataStr.startsWith('Error:')) {
                    console.error(dataStr);
                    setButtonsEnabled(true);
                }
                break;
            case 'ping':
                break;
            default:
                break;
        }
    }

    function connect(agenticMode, universalMode, universalAIMode) {
        if (!characterID && !agenticMode && !universalMode && !universalAIMode) return;

        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        let wsUrl;
        if (agenticMode) {
            wsUrl = `${protocol}//${window.location.host}/ws/agentic/try`;
        } else if (universalMode) {
            wsUrl = `${protocol}//${window.location.host}/ws/universal-tts/try`;
        } else if (universalAIMode) {
            wsUrl = `${protocol}//${window.location.host}/ws/universal-ai/try`;
        } else {
            wsUrl = `${protocol}//${window.location.host}/ws/characters/${characterID}/try`;
        }

        ws = new WebSocket(wsUrl);
        ws.binaryType = 'arraybuffer';

        ws.onopen = function () {
            console.log('try socket open');
            setButtonsEnabled(true);
        };

        ws.onerror = function () {
            try { ws.close(); } catch (e) { }
        };

        ws.onclose = function () {
            setButtonsEnabled(false);
            const stopBtn = document.getElementById("stop_button");
            if (stopBtn) stopBtn.disabled = false;

            const container = document.getElementById('tab-content');
            const currentContainer = container?.querySelector('[data-try-page="true"]');
            if (!currentContainer) return;

            if (agenticMode && currentContainer.dataset.agenticMode === "true") {
                setTimeout(() => connect(true, false, false), 1000);
            } else if (universalMode && currentContainer.dataset.universalMode === "true") {
                setTimeout(() => connect(false, true, false), 1000);
            } else if (universalAIMode && currentContainer.dataset.universalAiMode === "true") {
                setTimeout(() => connect(false, false, true), 1000);
            } else if (currentContainer.dataset.characterId === characterID) {
                setTimeout(() => connect(false, false, false), 1000);
            }
        };

        ws.onmessage = function (event) {
            const bytes = new Uint8Array(event.data);
            if (bytes.length === 0) return;

            // binary audio frame vs JSON envelope: frames start with 1/2/3,
            // JSON with '{'
            if (bytes[0] !== 0x7B) {
                try {
                    ensurePlayer().handleFrame(event.data);
                } catch (e) {
                    console.error('bad audio frame', e);
                }
                return;
            }

            try {
                handleEnvelope(JSON.parse(new TextDecoder('utf-8').decode(bytes)));
            } catch (e) {
                console.error('bad message', e);
            }
        };
    }

    function init() {
        const container = document.getElementById('tab-content');
        const tryContainer = container?.querySelector('[data-try-page="true"]');
        if (!tryContainer) return;

        cleanup();

        const agenticMode = tryContainer.dataset.agenticMode === "true";
        const universalMode = tryContainer.dataset.universalMode === "true";
        const universalAIMode = tryContainer.dataset.universalAiMode === "true";
        characterID = tryContainer.dataset.characterId;

        if (!characterID && !agenticMode && !universalMode && !universalAIMode) return;

        const bindings = [
            ["tts_button", 'tts'],
            ["universal_tts_button", 'universal_tts'],
            ["universal_ai_button", 'universal_ai'],
            ["ai_button", 'ai'],
            ["agentic_button", 'agentic'],
        ];
        for (const [id, action] of bindings) {
            const btn = document.getElementById(id);
            if (btn) btn.addEventListener('click', () => sendAction(action));
        }

        const stopBtn = document.getElementById("stop_button");
        if (stopBtn) stopBtn.addEventListener('click', stopCurrentAction);

        const input = document.getElementById("message_input");
        if (input) {
            input.addEventListener('input', () => {
                input.style.height = 'auto';
                input.style.height = input.scrollHeight + 'px';
            });
            input.focus();
        }

        setButtonsEnabled(false);
        connect(agenticMode, universalMode, universalAIMode);
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }

    document.body.addEventListener('htmx:afterSwap', function (event) {
        if (event.detail.target.querySelector('[data-try-page="true"]')) {
            init();
        }
    });

    document.body.addEventListener('htmx:beforeSwap', function () {
        if (document.querySelector('[data-try-page="true"]')) {
            cleanup();
        }
    });
})();
