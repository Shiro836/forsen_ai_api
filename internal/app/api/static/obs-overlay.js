// Overlay v2 (see adr/overlay-v2.md).
//
// Two websockets: /ws (JSON control: track_meta, skip, snapshot, images,
// clean, reload, ping + client actions) and /ws-audio (binary frames:
// [1B type][4B BE header len][header JSON][mp3]). Audio chunks are
// self-contained — text, word timings and track offset ride in the header —
// so a mid-track (re)connect can join at any chunk. Karaoke paints against
// the AudioContext clock, never against event arrival.
//
// State machine per track: unseen -> buffering -> active -> done, terminal
// skipped. Any socket loss = full reset of both sockets and all state.

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

    // sources -> masterGain -> analyser -> destination; the analyser drives
    // the character's voice pulse
    const masterGain = audioContext.createGain();
    const analyser = audioContext.createAnalyser();
    analyser.fftSize = 512;
    masterGain.connect(analyser);
    analyser.connect(audioContext.destination);
    const analyserData = new Uint8Array(analyser.fftSize);

    const captionCard = document.getElementById('caption_card');
    const captionWindow = document.getElementById('caption_window');
    const captionText = document.getElementById('caption_text');
    const charAnchor = document.getElementById('char_anchor');
    const charImg = document.getElementById('char_image');
    const imagesContainer = document.getElementById('images_container');

    // ---- state ----

    let controlWs = null;
    let audioWs = null;
    let resetting = false;
    let generation = 0; // bumped on every reset; async decodes check it

    const pendingSkips = new Set(); // msg_id, never pruned (reset clears)
    const tracks = new Map();       // track_id -> track
    const playQueue = [];           // track_ids in first-chunk arrival order
    let activeTrackId = null;

    let showImages = false;
    let currentImageURLs = [];

    function newTrack(trackId, msgId) {
        return {
            id: trackId,
            msgId: msgId,
            chunks: [],       // received, not yet scheduled (buffering)
            sources: [],      // live AudioBufferSourceNodes
            words: [],        // {w, s, e, el}
            done: false,
            queued: false,    // in playQueue (a meta-created track isn't, until audio arrives)
            totalDurMs: 0,
            maxChunkEndMs: 0,
            anchor: 0,        // audioContext.currentTime for track ms 0
        };
    }

    function ensureTrack(trackId, msgId) {
        let t = tracks.get(trackId);
        if (!t) {
            t = newTrack(trackId, msgId);
            tracks.set(trackId, t);
        }
        return t;
    }

    // ---- caption rendering ----

    function clearCaption() {
        captionText.innerHTML = '';
        captionText.style.transform = 'translateY(0)';
        captionCard.classList.remove('visible');
    }

    function appendWords(track, words) {
        for (const w of words) {
            const el = document.createElement('span');
            el.className = 'w';
            el.textContent = w.w + ' ';
            captionText.appendChild(el);
            track.words.push({ w: w.w, s: w.s, e: w.e, el: el });
        }
        captionCard.classList.add('visible');
    }

    // keep the active word inside the 3-line window: shift the text up so the
    // active line is the window's last visible line
    function scrollCaptionTo(el) {
        const lineHeight = el.offsetHeight || 1;
        const windowHeight = captionWindow.clientHeight;
        const shift = Math.max(0, el.offsetTop + lineHeight - windowHeight);
        captionText.style.transform = `translateY(${-shift}px)`;
    }

    // ---- playback ----

    function trackTimeMs(track) {
        return (audioContext.currentTime - track.anchor) * 1000;
    }

    function scheduleChunk(track, chunk) {
        const gen = generation;
        audioContext.decodeAudioData(chunk.mp3, (buffer) => {
            if (gen !== generation) return;
            if (pendingSkips.has(track.msgId)) return;
            if (tracks.get(track.id) !== track) return;

            const source = audioContext.createBufferSource();
            source.buffer = buffer;
            source.connect(masterGain);

            const startAt = Math.max(audioContext.currentTime, track.anchor + chunk.offsetMs / 1000);
            source.start(startAt);
            track.sources.push(source);
        }, (err) => {
            console.error('chunk decode failed', err);
        });

        track.maxChunkEndMs = Math.max(track.maxChunkEndMs, chunk.offsetMs + chunk.durMs);
        appendWords(track, chunk.words || []);
    }

    function activateTrack(trackId) {
        const track = tracks.get(trackId);
        if (!track) return;

        console.log('activate track', trackId.slice(0, 8));
        activeTrackId = trackId;
        clearCaption();
        lastActiveEl = null;
        track.words = [];

        // anchor so that the first buffered chunk plays immediately and the
        // karaoke clock is correct even when joining mid-track (offset > 0)
        const firstOffset = track.chunks.length ? track.chunks[0].offsetMs : 0;
        track.anchor = audioContext.currentTime + 0.05 - firstOffset / 1000;

        for (const chunk of track.chunks) {
            scheduleChunk(track, chunk);
        }
        track.chunks = [];
    }

    function tryActivate() {
        while (activeTrackId === null && playQueue.length > 0) {
            const next = playQueue.shift();
            if (!tracks.has(next)) continue;
            activateTrack(next);
        }
    }

    function finishTrack(track) {
        // caption stays on screen until the next track or a clean/skip
        for (const w of track.words) {
            w.el.classList.remove('active');
            w.el.classList.add('spoken');
        }
        tracks.delete(track.id);
        if (activeTrackId === track.id) {
            activeTrackId = null;
        }
        tryActivate();
    }

    function killTracksOfMsg(msgId) {
        let killedActive = false;
        for (const [id, track] of tracks) {
            if (track.msgId !== msgId) continue;
            for (const src of track.sources) {
                try { src.stop(); } catch (e) { }
            }
            tracks.delete(id);
            const qi = playQueue.indexOf(id);
            if (qi >= 0) playQueue.splice(qi, 1);
            if (activeTrackId === id) {
                activeTrackId = null;
                killedActive = true;
            }
        }
        return killedActive;
    }

    function skip(msgId, wipe) {
        pendingSkips.add(msgId);
        const killedActive = killTracksOfMsg(msgId);

        if (killedActive || wipe) {
            clearCaption();
            setCharImage("");
        }
        tryActivate();
    }

    function clearStage() {
        clearCaption();
        setCharImage("");
        currentImageURLs = [];
        showImages = false;
        renderPromptImages();
    }

    // ---- character & prompt images ----

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

    function activeMsgId() {
        const track = tracks.get(activeTrackId);
        return track ? track.msgId : null;
    }

    // ---- render loop: karaoke highlight + voice pulse + end detection ----

    let lastActiveEl = null;
    let pulse = 1;

    function renderLoop() {
        const track = tracks.get(activeTrackId);
        if (track) {
            const t = trackTimeMs(track);
            let activeEl = null;

            for (const w of track.words) {
                if (t >= w.e) {
                    if (!w.el.classList.contains('spoken')) {
                        w.el.classList.remove('active');
                        w.el.classList.add('spoken');
                    }
                } else if (t >= w.s) {
                    activeEl = w.el;
                    if (!w.el.classList.contains('active')) {
                        w.el.classList.add('active');
                        w.el.classList.remove('spoken');
                    }
                }
            }

            if (activeEl && activeEl !== lastActiveEl) {
                scrollCaptionTo(activeEl);
                lastActiveEl = activeEl;
            }

            const endMs = track.done ? Math.max(track.totalDurMs, track.maxChunkEndMs) : Infinity;
            if (t >= endMs) {
                finishTrack(track);
            }
        }

        // voice pulse from output RMS
        analyser.getByteTimeDomainData(analyserData);
        let sum = 0;
        for (let i = 0; i < analyserData.length; i++) {
            const v = (analyserData[i] - 128) / 128;
            sum += v * v;
        }
        const rms = Math.sqrt(sum / analyserData.length);
        const target = 1 + Math.min(rms * 0.1, 0.025);
        pulse += (target - pulse) * 0.3;
        charAnchor.style.setProperty('--pulse', pulse.toFixed(4));

        requestAnimationFrame(renderLoop);
    }
    requestAnimationFrame(renderLoop);

    // ---- audio socket frame handling ----

    function handleAudioFrame(buf) {
        const view = new DataView(buf);
        const frameType = view.getUint8(0);
        if (frameType === 3) return; // ping

        const headerLen = view.getUint32(1, false);
        const headerBytes = new Uint8Array(buf, 5, headerLen);
        const header = JSON.parse(new TextDecoder('utf-8').decode(headerBytes));

        if (frameType === 2) { // track_done
            // the bus does not guarantee cross-frame order: done can overtake
            // the first chunk, so it must create the track rather than drop
            const track = ensureTrack(header.track_id, header.msg_id);
            track.done = true;
            track.totalDurMs = header.total_dur_ms || 0;
            return;
        }

        if (frameType !== 1) return;

        if (pendingSkips.has(header.msg_id)) return;

        const mp3 = buf.slice(5 + headerLen);
        const chunk = {
            offsetMs: header.offset_ms || 0,
            durMs: header.dur_ms || 0,
            text: header.text || '',
            words: header.words || [],
            mp3: mp3,
        };

        console.log('chunk', header.track_id.slice(0, 8), 'seq', header.seq, 'offset', header.offset_ms, 'words', (header.words || []).length);

        const track = ensureTrack(header.track_id, header.msg_id);

        if (activeTrackId === track.id) {
            scheduleChunk(track, chunk);
        } else {
            track.chunks.push(chunk);
            if (!track.queued) {
                track.queued = true;
                playQueue.push(track.id);
            }
        }

        tryActivate();
    }

    // ---- control socket event handling ----

    function handleControlEvent(msg) {
        const dataStr = new TextDecoder('utf-8').decode(base64ToArrayBuffer(msg.data));

        switch (msg.type) {
            case 'track_meta': {
                // registers the track -> msg mapping ahead of audio; words ride
                // in the chunks themselves
                try {
                    const meta = JSON.parse(dataStr);
                    if (!pendingSkips.has(meta.msg_id)) {
                        ensureTrack(meta.track_id, meta.msg_id);
                    }
                } catch (e) { }
                break;
            }
            case 'snapshot': {
                try {
                    const snap = JSON.parse(dataStr);
                    for (const id of (snap.skipped || [])) {
                        pendingSkips.add(id);
                    }
                } catch (e) { }
                break;
            }
            case 'skip': {
                try {
                    const payload = JSON.parse(dataStr);
                    skip(payload.msg_id, payload.current !== false);
                } catch (e) {
                    skip(dataStr, true);
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
                    renderPromptImages();
                } catch (e) {
                    console.error('failed to parse prompt_image payload', e);
                }
                break;
            }
            case 'show_images':
                if (activeMsgId() === dataStr) {
                    showImages = true;
                    renderPromptImages();
                }
                break;
            case 'hide_images':
                if (activeMsgId() === dataStr) {
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

    // ---- sockets: any loss resets everything ----

    function fullReset() {
        if (resetting) return;
        resetting = true;
        generation++;

        for (const [, track] of tracks) {
            for (const src of track.sources) {
                try { src.stop(); } catch (e) { }
            }
        }
        tracks.clear();
        playQueue.length = 0;
        activeTrackId = null;
        pendingSkips.clear();
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
            skipHandler = () => {
                controlWs.send(JSON.stringify({ action: 'skip', token: token }));
            };
            showImagesHandler = () => {
                controlWs.send(JSON.stringify({ action: 'show_images', token: token }));
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
                handleAudioFrame(event.data);
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
