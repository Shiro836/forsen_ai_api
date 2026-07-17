// OBS overlay page glue for overlay-v2 (see adr/overlay-v2.md). The playback
// engine (tracks, karaoke, audio scheduling) lives in overlay-player.js; this
// file owns the two websockets, the character/prompt images and the hotkeys.
//
// Two websockets: /ws (JSON control: track_meta, skip, snapshot, images,
// clean, reload, ping + client actions) and /ws-audio (binary frames). Any
// socket loss = full reset of both sockets and all state.

let audioContext;
let token = "";
let currentToken = "";
let isCapturingToken = false;

const startKey = '0';
const endKey = '1';
const skipKey = '2';
const showImagesKey = '3';

let skipHandler = null;
let showImagesHandler = null;

document.documentElement.addEventListener('click', () => {
    if (audioContext && audioContext.state === 'suspended') {
        audioContext.resume();
    }
});

window.addEventListener('keydown', (event) => {
    const key = event.key;

    if (key === skipKey) {
        if (skipHandler) skipHandler();
        return;
    }
    if (key === showImagesKey) {
        if (showImagesHandler) showImagesHandler();
        return;
    }
    if (key === startKey) {
        currentToken = "";
        isCapturingToken = true;
        return;
    }
    if (key === endKey) {
        if (isCapturingToken) {
            isCapturingToken = false;
            token = currentToken;
            console.log('Token captured');
        }
        return;
    }
    if (isCapturingToken && key.length === 1) {
        currentToken += key;
    }
});

function base64ToArrayBuffer(base64) {
    const binaryString = window.atob(base64);
    const bytes = new Uint8Array(binaryString.length);
    for (let i = 0; i < binaryString.length; i++) {
        bytes[i] = binaryString.charCodeAt(i);
    }
    return bytes.buffer;
}

async function pageReady() {
    audioContext = new (window.AudioContext || window.webkitAudioContext)();

    const player = new OverlayPlayer({
        audioContext: audioContext,
        cardEl: document.getElementById('caption_card'),
        windowEl: document.getElementById('caption_window'),
        textEl: document.getElementById('caption_text'),
    });

    const charAnchor = document.getElementById('char_anchor');
    const charImg = document.getElementById('char_image');
    const imagesContainer = document.getElementById('images_container');

    let controlWs = null;
    let audioWs = null;
    let resetting = false;

    let showImages = false;
    let currentImageURLs = [];
    // msg_id set mirroring pendingSkips: show/hide clicks can land while the
    // message isn't audibly playing yet, so intent is remembered and applied
    // when its track activates (reset clears, snapshot restores)
    const shownImages = new Set();

    function setCharImage(url) {
        showImages = false;
        if (!url || url.length <= 1) {
            charImg.classList.remove('visible');
            currentImageURLs = [];
            renderPromptImages();
            return;
        }
        renderPromptImages();
        charImg.src = url;
        charImg.classList.add('visible');
    }

    function renderPromptImages() {
        imagesContainer.innerHTML = '';
        if (!showImages || currentImageURLs.length === 0) {
            imagesContainer.style.display = 'none';
            return;
        }
        for (const raw of currentImageURLs) {
            const slot = document.createElement('div');
            slot.className = 'img_slot';
            const img = document.createElement('img');
            img.src = raw.startsWith('http') ? raw : (window.location.origin + raw);
            slot.appendChild(img);
            imagesContainer.appendChild(slot);
        }
        imagesContainer.style.display = 'flex';
    }

    function clearStage() {
        player.clearCaption();
        setCharImage("");
        currentImageURLs = [];
        showImages = false;
        renderPromptImages();
    }

    // a track activating is the sync point where remembered intent wins over
    // whatever the earlier prompt_image baseline said
    player.onTrackActivated = (msgId) => {
        const shouldShow = shownImages.has(msgId);
        if (shouldShow !== showImages) {
            showImages = shouldShow;
            renderPromptImages();
        }
    };

    // voice pulse: character breathes with the output level. The peak is
    // clamped against the caption card's bottom edge (its height varies with
    // line count), so the head never breathes into the text box.
    let pulse = 1;
    (function pulseLoop() {
        let maxExtra = 0.05;
        if (player.cardEl.classList.contains('visible')) {
            const baseHeight = charAnchor.getBoundingClientRect().height / pulse;
            if (baseHeight > 0) {
                const allowedHeight = window.innerHeight - player.cardEl.getBoundingClientRect().bottom - 8;
                maxExtra = Math.max(0, Math.min(maxExtra, allowedHeight / baseHeight - 1));
            }
        }
        const target = 1 + Math.min(player.level() * 0.2, maxExtra);
        pulse += (target - pulse) * 0.3;
        charAnchor.style.setProperty('--pulse', pulse.toFixed(4));
        requestAnimationFrame(pulseLoop);
    })();

    function handleControlEvent(msg) {
        const dataStr = new TextDecoder('utf-8').decode(base64ToArrayBuffer(msg.data));

        switch (msg.type) {
            case 'track_meta': {
                try {
                    const meta = JSON.parse(dataStr);
                    player.registerMeta(meta.track_id, meta.msg_id);
                } catch (e) { }
                break;
            }
            case 'snapshot': {
                try {
                    const snap = JSON.parse(dataStr);
                    player.addPendingSkips(snap.skipped);
                    for (const id of snap.shown_images || []) shownImages.add(id);
                } catch (e) { }
                break;
            }
            case 'skip': {
                let msgId = dataStr;
                let wipe = true;
                try {
                    const payload = JSON.parse(dataStr);
                    if (payload && payload.msg_id) {
                        msgId = payload.msg_id;
                        wipe = payload.current !== false;
                    }
                } catch (e) { }
                if (player.skip(msgId, wipe)) {
                    setCharImage("");
                }
                break;
            }
            case 'clean':
                clearStage();
                break;
            case 'reload':
                location.reload();
                break;
            case 'image':
                setCharImage(dataStr);
                break;
            case 'prompt_image': {
                try {
                    const payload = JSON.parse(dataStr);
                    currentImageURLs = Array.isArray(payload.image_ids) ? payload.image_ids.map(id => `/images/${id}`) : [];
                    showImages = !!payload.show_images;
                    if (payload.msg_id) {
                        if (showImages) shownImages.add(payload.msg_id);
                        else shownImages.delete(payload.msg_id);
                    }
                    renderPromptImages();
                } catch (e) {
                    console.error('failed to parse prompt_image payload', e);
                }
                break;
            }
            case 'show_images':
                shownImages.add(dataStr);
                if (player.activeMsgId() === dataStr) {
                    showImages = true;
                    renderPromptImages();
                }
                break;
            case 'hide_images':
                shownImages.delete(dataStr);
                if (player.activeMsgId() === dataStr) {
                    showImages = false;
                    renderPromptImages();
                }
                break;
            case 'ping':
                break;
            default:
                // unknown types are future protocol: ignore silently
                break;
        }
    }

    function fullReset() {
        if (resetting) return;
        resetting = true;

        player.reset();
        shownImages.clear();
        clearStage();

        skipHandler = null;
        showImagesHandler = null;

        try { if (controlWs) controlWs.close(); } catch (e) { }
        try { if (audioWs) audioWs.close(); } catch (e) { }
        controlWs = null;
        audioWs = null;

        setTimeout(() => {
            resetting = false;
            connect();
        }, 1000);
    }

    function connect() {
        const base = `wss://${window.location.host}`;
        const path = window.location.pathname;

        controlWs = new WebSocket(`${base}/ws${path}`);
        controlWs.binaryType = 'arraybuffer';

        controlWs.onopen = () => {
            console.log('control socket open');
            // msg_id pins the action to what is audibly playing: the server's
            // notion of "current" can already be the next message while the
            // overlay still speaks this one
            skipHandler = () => {
                controlWs.send(JSON.stringify({ action: 'skip', token: token, msg_id: player.activeMsgId() || '' }));
            };
            showImagesHandler = () => {
                controlWs.send(JSON.stringify({ action: 'show_images', token: token, msg_id: player.activeMsgId() || '' }));
            };
        };
        controlWs.onmessage = (event) => {
            try {
                const decoded = new TextDecoder('utf-8').decode(new Uint8Array(event.data));
                handleControlEvent(JSON.parse(decoded));
            } catch (e) {
                console.error('bad control message', e);
            }
        };
        controlWs.onerror = () => { try { controlWs.close(); } catch (e) { } };
        controlWs.onclose = () => {
            console.log('control socket closed, resetting');
            fullReset();
        };

        audioWs = new WebSocket(`${base}/ws-audio${path}`);
        audioWs.binaryType = 'arraybuffer';

        audioWs.onopen = () => {
            console.log('audio socket open');
        };
        audioWs.onmessage = (event) => {
            try {
                player.handleFrame(event.data);
            } catch (e) {
                console.error('bad audio frame', e);
            }
        };
        audioWs.onerror = () => { try { audioWs.close(); } catch (e) { } };
        audioWs.onclose = () => {
            console.log('audio socket closed, resetting');
            fullReset();
        };
    }

    connect();
}
