// Try page (character / agentic / universal TTS) on the overlay-v2 protocol.
// Audio arrives as raw binary frames on the same websocket that carries the
// JSON envelope; the first byte tells them apart ('{' vs frame type 1/2/3).
// Playback and karaoke are the same OverlayPlayer the OBS overlay uses.

(function () {
    let ws = null;
    let audioContext = null;
    let player = null;
    let characterID = null;

    function cleanup() {
        if (ws) {
            ws.close();
            ws = null;
        }
        if (player) {
            player.destroy();
            player = null;
        }
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

    function setButtonsEnabled(enabled) {
        for (const id of ["tts_button", "universal_tts_button", "ai_button", "agentic_button"]) {
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
        ws.send(JSON.stringify({ action: action, text: text }));
        input.focus();
    }

    function handleEnvelope(msg) {
        const dataStr = new TextDecoder('utf-8').decode(base64ToArrayBuffer(msg.data));

        switch (msg.type) {
            case 'track_meta': {
                try {
                    const meta = JSON.parse(dataStr);
                    ensurePlayer().registerMeta(meta.track_id, meta.msg_id);
                } catch (e) { }
                break;
            }
            case 'skip': {
                let msgId = dataStr;
                try {
                    const payload = JSON.parse(dataStr);
                    if (payload && payload.msg_id) msgId = payload.msg_id;
                } catch (e) { }
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

    function connect(agenticMode, universalMode) {
        if (!characterID && !agenticMode && !universalMode) return;

        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        let wsUrl;
        if (agenticMode) {
            wsUrl = `${protocol}//${window.location.host}/ws/agentic/try`;
        } else if (universalMode) {
            wsUrl = `${protocol}//${window.location.host}/ws/universal-tts/try`;
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
                setTimeout(() => connect(true, false), 1000);
            } else if (universalMode && currentContainer.dataset.universalMode === "true") {
                setTimeout(() => connect(false, true), 1000);
            } else if (currentContainer.dataset.characterId === characterID) {
                setTimeout(() => connect(false, false), 1000);
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
        characterID = tryContainer.dataset.characterId;

        if (!characterID && !agenticMode && !universalMode) return;

        const bindings = [
            ["tts_button", 'tts'],
            ["universal_tts_button", 'universal_tts'],
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
        connect(agenticMode, universalMode);
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
