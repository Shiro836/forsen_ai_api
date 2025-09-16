let serverConnection;
let audioContext;

document.documentElement.addEventListener('click', () => {
    if (audioContext.state === 'suspended') {
        audioContext.resume();
    }

    if (!audioContext) {
        audioContext = new (window.AudioContext || window.webkitAudioContext)();
    }
});

function base64ToArrayBuffer(base64) {
    var binaryString = window.atob(base64);
    var len = binaryString.length;
    var bytes = new Uint8Array(len);
    for (var i = 0; i < len; i++) {
        bytes[i] = binaryString.charCodeAt(i);
    }

    return bytes.buffer;
}

async function pageReady() {
    audioContext = new (window.AudioContext || window.webkitAudioContext)({});

    const audio_sources = new Map();
    const pending_skips = new Set();

    // Track current message and image visibility
    let currentMsgId = null;
    let showImages = false;
    let currentImageURLs = [];

    function playWavFile(arrayBuffer, msg_id) {
        // If skip already requested for this message, do not start playback
        if (pending_skips.has(msg_id)) {
            return;
        }

        audioContext.decodeAudioData(arrayBuffer, function (buffer) {
            // Check skip after decode
            if (pending_skips.has(msg_id)) {
                return;
            }

            const source = audioContext.createBufferSource();
            source.buffer = buffer;
            source.channelCount = 1;
            source.connect(audioContext.destination);

            source.onended = () => {
                currentMsgId = null;
                audio_sources.delete(msg_id);
            };

            audio_sources.set(msg_id, source);
            source.start();
            currentMsgId = msg_id;
        });
    }

    let currentResponse = "";
    function updateText(responseText) {
        if (!responseText) textBox.innerHTML = ""

        let text = responseText;
        const responseBox = document.getElementById("response_box");
        const textBox = document.getElementById("text_box");

        if (responseText.startsWith(currentResponse)) {
            text = responseText.slice(currentResponse.length);
        } else {
            textBox.innerHTML = "";
        }
        currentResponse = responseText;

        const args = text.split(" ");
        let i = 0;
        const typeWriter = () => {
            if (i < args.length) {
                let word = args[i];
                if (args.length > i + 1) {
                    word += ' ';
                }

                const wordEl = document.createElement("span");
                wordEl.innerText = word;
                textBox.appendChild(wordEl);
                responseBox.style.height = textBox.clientHeight + "px";
                responseBox.scrollTo({ top: responseBox.scrollHeight });

                i++;
                setTimeout(typeWriter, 200);
            }
        }
        typeWriter();
    }

    function set_image(url) {
        const charImg = document.getElementById("char_image");
        showImages = false;

        if (!url || url.length <= 1) {
            charImg.style.opacity = "0";
            // also clear any prompt images
            currentImageURLs = [];
            renderPromptImages();
            return;
        }

        renderPromptImages();
        charImg.src = url;
        charImg.style.opacity = "1";
    }

    function renderPromptImages() {
        const imagesContainer = document.getElementById('images_container');
        imagesContainer.innerHTML = '';
        if (!showImages || currentImageURLs.length === 0) {
            imagesContainer.style.display = 'none';
            return;
        }
        // Set image count for CSS to size evenly without distorting ratios
        imagesContainer.style.setProperty('--img-count', String(currentImageURLs.length));
        for (let i = 0; i < currentImageURLs.length; i++) {
            const slot = document.createElement('div');
            slot.className = 'img_slot';
            const img = document.createElement('img');
            // Ensure absolute URL for overlay context
            const url = currentImageURLs[i].startsWith('http') ? currentImageURLs[i] : (window.location.origin + currentImageURLs[i]);
            img.src = url;
            slot.appendChild(img);
            imagesContainer.appendChild(slot);
        }
        imagesContainer.style.display = 'flex';
        console.log('showed images ', String(currentImageURLs.length));
    }

    function skip(msg_id) {
        console.log('skip requested for:', msg_id);
        console.log('current audio sources:', audio_sources);
        
        pending_skips.add(msg_id);

        const src = audio_sources.get(msg_id);
        if (src) {
            try { src.stop(); } catch (e) {}
            audio_sources.delete(msg_id);
            updateText(" ");
            set_image("");
        }
    }

    function connect() {
        ws = new WebSocket(`wss://${window.location.host + "/ws" + window.location.pathname}`);
        ws.binaryType = 'arraybuffer'

        ws.onerror = function (err) {
            console.error('Socket encountered error: ', err.message, 'Closing socket');
            ws.close();
        };

        ws.onclose = function (e) {
            console.log('Socket is closed. Reconnect will be attempted in 1 second.', e.reason);

            // stop all current audio to avoid orphan playback
            for (const [id, src] of audio_sources.entries()) {
                try { src.stop(); } catch (err) {}
                audio_sources.delete(id);
            }
            // clear UI
            updateText(" ");
            set_image("");

            setTimeout(function () {
                connect();
            }, 1000);
        };

        ws.onmessage = function (event) {
            let uint8Array = new Uint8Array(event.data);

            let decoder = new TextDecoder('utf-8');
            let utf8String = decoder.decode(uint8Array);

            msg = JSON.parse(utf8String)

            data = base64ToArrayBuffer(msg.data);
            dataStr = decoder.decode(data);

            switch (msg.type) {
                case 'text':
                    updateText(dataStr);
                    break
                case 'audio':
                    dataJson = JSON.parse(dataStr);
                    playWavFile(base64ToArrayBuffer(dataJson.audio), dataJson.msg_id);
                    break
                case 'image':
                    set_image(dataStr)
                    break
                case 'prompt_image':
                    try {
                        const payload = JSON.parse(dataStr);
                        currentImageURLs = Array.isArray(payload.image_ids) ? payload.image_ids.map(id => `/images/${id}`) : [];
                        // Reset visibility per-message; default to false if missing
                        console.log('showImages', payload.show_images);
                        showImages = !!payload.show_images;
                        console.log('showImages', showImages);
                        // Optionally track msg id if provided later; default to null now
                        renderPromptImages();
                    } catch (e) {
                        console.error('failed to parse prompt_image payload', e);
                    }
                    break
                case 'show_images':
                    msgID = dataStr;
                    // msg data is message id to show for; no-op for id since overlay only shows current
                    if (currentMsgId !== msgID) {
                        return;
                    }
                    showImages = true;
                    renderPromptImages();
                    break
                case 'hide_images':
                    msgID = dataStr;
                    if (currentMsgId !== msgID) {
                        return;
                    }
                    showImages = false;
                    renderPromptImages();
                    break
                case 'skip':
                    skip(dataStr)
                    break
                case 'ping':
                    break
                default:
                    console.log('unknown type')
                    break;
            }
        };
        // clear any pending skip markers on a fresh connection
        pending_skips.clear();
    }

    connect();
}

