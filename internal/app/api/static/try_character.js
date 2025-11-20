// Try Character functionality
(function () {
    let ws = null;
    let audioContext = null;
    let currentResponse = "";
    let typewriterTimeoutId = null;
    let stopTypewriter = false;
    let characterID = null;

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

    function playWavFile(arrayBuffer) {
        if (!audioContext) {
            audioContext = new (window.AudioContext || window.webkitAudioContext)();
        }
        if (audioContext.state === 'suspended') {
            audioContext.resume();
        }

        audioContext.decodeAudioData(arrayBuffer, function (buffer) {
            const source = audioContext.createBufferSource();
            source.buffer = buffer;
            source.channelCount = 1;
            source.connect(audioContext.destination);
            source.start();
        });
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
        const aiBtn = document.getElementById("ai_button");
        if (ttsBtn) ttsBtn.disabled = false;
        if (aiBtn) aiBtn.disabled = false;
    }

    function disableButtons() {
        const ttsBtn = document.getElementById("tts_button");
        const aiBtn = document.getElementById("ai_button");
        if (ttsBtn) ttsBtn.disabled = true;
        if (aiBtn) aiBtn.disabled = true;
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
    }

    function connect() {
        if (!characterID) return;

        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const wsUrl = `${protocol}//${window.location.host}/ws/characters/${characterID}/try`;

        console.log('Connecting to:', wsUrl);
        ws = new WebSocket(wsUrl);
        ws.binaryType = 'arraybuffer';

        ws.onopen = function () {
            console.log('WebSocket connected');
            enableButtons();
        };

        ws.onerror = function (err) {
            console.error('WebSocket error:', err);
            ws.close();
        };

        ws.onclose = function (e) {
            console.log('WebSocket closed.', e.reason);
            disableButtons();

            // Only reconnect if we're still on the try page
            const container = document.getElementById('tab-content');
            if (container?.querySelector('[data-character-id]')?.dataset.characterId === characterID) {
                setTimeout(connect, 1000);
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
                case 'text':
                    if (!dataStr || dataStr.trim() === '') {
                        stopTypewriter = true;
                        if (typewriterTimeoutId) {
                            clearTimeout(typewriterTimeoutId);
                            typewriterTimeoutId = null;
                        }
                        const textBox = document.getElementById("text_box");
                        if (textBox) {
                            textBox.innerHTML = "";
                            currentResponse = "";
                        }
                    } else {
                        updateText(dataStr);
                    }
                    break;
                case 'audio':
                    const dataJson = JSON.parse(dataStr);
                    playWavFile(base64ToArrayBuffer(dataJson.audio));
                    break;
                case 'image':
                    setImage(dataStr);
                    break;
                case 'ping':
                    break;
                default:
                    console.log('Unknown message type:', msg.type);
                    break;
            }
        };
    }

    function init() {
        const container = document.getElementById('tab-content');
        const tryContainer = container?.querySelector('[data-character-id]');

        if (!tryContainer) return;

        // Cleanup any previous connection
        cleanup();

        characterID = tryContainer.dataset.characterId;
        if (!characterID) return;

        console.log('Initializing try character for:', characterID);

        // Initialize audio context on first user interaction
        const initAudio = () => {
            if (!audioContext) {
                audioContext = new (window.AudioContext || window.webkitAudioContext)();
            }
            if (audioContext.state === 'suspended') {
                audioContext.resume();
            }
        };

        // Set up event listeners
        const ttsBtn = document.getElementById("tts_button");
        const aiBtn = document.getElementById("ai_button");
        const input = document.getElementById("message_input");

        if (ttsBtn) {
            ttsBtn.addEventListener('click', () => {
                initAudio();
                sendAction('tts');
            });
        }

        if (aiBtn) {
            aiBtn.addEventListener('click', () => {
                initAudio();
                sendAction('ai');
            });
        }

        if (input) {
            input.addEventListener('keypress', (e) => {
                if (e.key === 'Enter') {
                    initAudio();
                    sendAction('ai');
                }
            });
        }

        // Initial state
        disableButtons();
        connect();
    }

    // Initialize on page load
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }

    // Handle htmx content swaps
    document.body.addEventListener('htmx:afterSwap', function (event) {
        // Check if the swapped content has our try character container
        if (event.detail.target.querySelector('[data-character-id]')) {
            init();
        }
    });

    // Clean up when navigating away
    document.body.addEventListener('htmx:beforeSwap', function (event) {
        const tryContainer = document.querySelector('[data-character-id]');
        if (tryContainer) {
            cleanup();
        }
    });
})();
