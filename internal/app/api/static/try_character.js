(function () {
    let ws = null;
    let audioContext = null;
    let currentResponse = "";
    let typewriterTimeoutId = null;
    let stopTypewriter = false;
    let characterID = null;
    const audioSources = new Map();
    const pending_skips = new Set();

    function cleanup() {
        console.log('Cleaning up try character');
        if (ws) {
            ws.close();
            ws = null;
        }
        if (typewriterTimeoutId) {
            clearTimeout(typewriterTimeoutId);
            typewriterTimeoutId = null;
        }
        for (const [id, source] of audioSources.entries()) {
            try {
                source.stop();
            } catch (err) {
                // Source may already be stopped
            }
            audioSources.delete(id);
        }
        currentResponse = "";
        stopTypewriter = false;
    }

    function base64ToArrayBuffer(base64) {
        const binaryString = window.atob(base64);
        const len = binaryString.length;
        const bytes = new Uint8Array(len);
        for (let i = 0; i < len; i++) {
            bytes[i] = binaryString.charCodeAt(i);
        }
        return bytes.buffer;
    }

    function playWavFile(arrayBuffer, msgId) {
        if (!audioContext) {
            audioContext = new (window.AudioContext || window.webkitAudioContext)();
        }
        if (audioContext.state === 'suspended') {
            audioContext.resume();
        }

        const audioId = msgId || `audio_${Date.now()}_${Math.random()}`;

        audioContext.decodeAudioData(arrayBuffer, function (buffer) {
            const source = audioContext.createBufferSource();
            source.buffer = buffer;
            source.channelCount = 1;
            source.connect(audioContext.destination);

            source.onended = () => {
                audioSources.delete(audioId);
            };

            audioSources.set(audioId, source);
            source.start();
        });
    }

    function stopAllAudio() {
        for (const [id, source] of audioSources.entries()) {
            try {
                source.stop();
            } catch (err) {
                // Source may already be stopped
            }
            audioSources.delete(id);
        }
    }

    function updateText(responseText) {
        if (typewriterTimeoutId) {
            clearTimeout(typewriterTimeoutId);
            typewriterTimeoutId = null;
        }
        stopTypewriter = false;

        const textBox = document.getElementById("text_box");
        if (!textBox) return;

        if (!responseText || responseText.trim() === '') {
            textBox.innerHTML = "";
            currentResponse = "";
            return;
        }

        let text = responseText;
        if (responseText.startsWith(currentResponse)) {
            text = responseText.slice(currentResponse.length);
        } else {
            textBox.innerHTML = "";
        }
        currentResponse = responseText;

        const args = text.split(" ");
        let i = 0;
        const typeWriter = () => {
            if (stopTypewriter || i >= args.length) {
                typewriterTimeoutId = null;
                return;
            }

            let word = args[i];
            if (args.length > i + 1) {
                word += ' ';
            }

            const wordEl = document.createElement("span");
            wordEl.innerText = word;
            textBox.appendChild(wordEl);

            i++;
            typewriterTimeoutId = setTimeout(typeWriter, 200);
        }
        typeWriter();
    }

    function setImage(url) {
        const charImg = document.getElementById("char_image");
        if (!charImg) return;

        // On try page, ignore empty image events to keep character image visible
        if (!url || url.trim() === '' || url.trim().length <= 1) {
            return;
        }

        charImg.src = url;
        charImg.style.opacity = "1";
    }

    function enableButtons() {
        const ttsBtn = document.getElementById("tts_button");
        const universalTtsBtn = document.getElementById("universal_tts_button");
        const aiBtn = document.getElementById("ai_button");
        const agenticBtn = document.getElementById("agentic_button");
        const stopBtn = document.getElementById("stop_button");
        if (ttsBtn) ttsBtn.disabled = false;
        if (universalTtsBtn) universalTtsBtn.disabled = false;
        if (aiBtn) aiBtn.disabled = false;
        if (agenticBtn) agenticBtn.disabled = false;
        if (stopBtn) stopBtn.disabled = false;
    }

    function disableButtons() {
        const ttsBtn = document.getElementById("tts_button");
        const universalTtsBtn = document.getElementById("universal_tts_button");
        const aiBtn = document.getElementById("ai_button");
        const agenticBtn = document.getElementById("agentic_button");
        const stopBtn = document.getElementById("stop_button");
        if (ttsBtn) ttsBtn.disabled = true;
        if (universalTtsBtn) universalTtsBtn.disabled = true;
        if (aiBtn) aiBtn.disabled = true;
        if (agenticBtn) agenticBtn.disabled = true;
        if (stopBtn) stopBtn.disabled = true;
    }

    function stopCurrentAction() {
        stopTypewriter = true;
        if (typewriterTimeoutId) {
            clearTimeout(typewriterTimeoutId);
            typewriterTimeoutId = null;
        }

        stopAllAudio();

        const textBox = document.getElementById("text_box");
        if (textBox) {
            textBox.innerHTML = "";
            currentResponse = "";
        }

        if (ws && ws.readyState === WebSocket.OPEN) {
            const message = JSON.stringify({
                action: "stop",
                text: ""
            });
            console.log('Sending stop:', message);
            ws.send(message);
        }

        enableButtons();
    }

    function sendAction(action) {
        const input = document.getElementById("message_input");
        if (!input) return;

        const text = input.value.trim();
        if (!text) {
            console.warn('Empty message');
            return;
        }

        if (!ws || ws.readyState !== WebSocket.OPEN) {
            console.error('WebSocket not connected');
            return;
        }

        const message = JSON.stringify({
            action: action,
            text: text
        });

        console.log('Sending:', message);
        ws.send(message);

        input.focus();
    }

    function autoResizeInput(input) {
        if (!input) return;
        input.style.height = 'auto';
        input.style.height = input.scrollHeight + 'px';
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

        console.log('Initializing try page. Character:', characterID, 'Agentic:', agenticMode, 'Universal:', universalMode);

        // Initialize audio context on first user interaction
        const initAudio = () => {
            if (!audioContext) {
                audioContext = new (window.AudioContext || window.webkitAudioContext)();
            }
            if (audioContext.state === 'suspended') {
                audioContext.resume();
            }
        };

        const ttsBtn = document.getElementById("tts_button");
        const universalTtsBtn = document.getElementById("universal_tts_button");
        const aiBtn = document.getElementById("ai_button");
        const agenticBtn = document.getElementById("agentic_button");
        const stopBtn = document.getElementById("stop_button");
        const input = document.getElementById("message_input");

        if (ttsBtn) {
            ttsBtn.addEventListener('click', () => {
                initAudio();
                sendAction('tts');
            });
        }

        if (universalTtsBtn) {
            universalTtsBtn.addEventListener('click', () => {
                initAudio();
                sendAction('universal_tts');
            });
        }

        if (aiBtn) {
            aiBtn.addEventListener('click', () => {
                initAudio();
                sendAction('ai');
            });
        }

        if (agenticBtn) {
            agenticBtn.addEventListener('click', () => {
                initAudio();
                sendAction('agentic');
            });
        }

        if (stopBtn) {
            stopBtn.addEventListener('click', () => {
                stopCurrentAction();
            });
        }

        if (input) {
            input.addEventListener('input', () => autoResizeInput(input));
            input.focus();
        }

        disableButtons();
        // Stop button should always be enabled
        if (stopBtn) stopBtn.disabled = false;

        connect(agenticMode, universalMode);
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

        console.log('Connecting to:', wsUrl);
        ws = new WebSocket(wsUrl);
        ws.binaryType = 'arraybuffer';

        ws.onopen = function () {
            console.log('WebSocket connected');
            enableButtons();
            // Stop button should always be enabled
            const stopBtn = document.getElementById("stop_button");
            if (stopBtn) stopBtn.disabled = false;
        };

        ws.onerror = function (err) {
            console.error('WebSocket error:', err);
            ws.close();
        };

        ws.onclose = function (e) {
            console.log('WebSocket closed.', e.reason);
            disableButtons();
            // Stop button should always be enabled
            const stopBtn = document.getElementById("stop_button");
            if (stopBtn) stopBtn.disabled = false;

            // Only reconnect if we're still on the try page
            const container = document.getElementById('tab-content');
            const currentContainer = container?.querySelector('[data-try-page="true"]');

            if (currentContainer) {
                if (agenticMode && currentContainer.dataset.agenticMode === "true") {
                    setTimeout(() => connect(true, false), 1000);
                } else if (universalMode && currentContainer.dataset.universalMode === "true") {
                    setTimeout(() => connect(false, true), 1000);
                } else if (currentContainer.dataset.characterId === characterID) {
                    setTimeout(() => connect(false, false), 1000);
                }
            }
        };

        ws.onmessage = function (event) {
            const uint8Array = new Uint8Array(event.data);
            const decoder = new TextDecoder('utf-8');
            const utf8String = decoder.decode(uint8Array);

            let msg;
            try {
                msg = JSON.parse(utf8String);
            } catch (e) {
                console.error('Failed to parse message:', e);
                return;
            }

            const data = base64ToArrayBuffer(msg.data);
            const dataStr = decoder.decode(data);

            switch (msg.type) {
                case 'text': {
                    let payload;
                    try { payload = JSON.parse(dataStr); } catch (e) { break; }
                    if (pending_skips.has(payload.msg_id)) {
                        break;
                    }
                    updateText(payload.text || "");
                    break;
                }
                case 'audio':
                    const dataJson = JSON.parse(dataStr);
                    playWavFile(base64ToArrayBuffer(dataJson.audio), dataJson.msg_id);
                    break;
                case 'image':
                    setImage(dataStr);
                    break;
                case 'skip':
                    let skipId = dataStr;
                    try {
                        const payload = JSON.parse(dataStr);
                        if (payload && payload.msg_id) {
                            skipId = payload.msg_id;
                        }
                    } catch (e) { }
                    pending_skips.add(skipId);
                    stopTypewriter = true;
                    if (typewriterTimeoutId) {
                        clearTimeout(typewriterTimeoutId);
                        typewriterTimeoutId = null;
                    }
                    stopAllAudio();
                    const textBox = document.getElementById("text_box");
                    if (textBox) {
                        textBox.innerHTML = "";
                        currentResponse = "";
                    }
                    enableButtons();
                    break;
                case 'ping':
                    break;
                default:
                    console.log('Unknown message type:', msg.type);
                    break;
            }
        };
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

    // Clean up when navigating away
    document.body.addEventListener('htmx:beforeSwap', function (event) {
        const tryContainer = document.querySelector('[data-try-page="true"]');
        if (tryContainer) {
            cleanup();
        }
    });
})();
