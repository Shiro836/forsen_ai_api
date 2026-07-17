// OverlayPlayer: the shared overlay-v2 playback engine (see adr/overlay-v2.md).
// Consumes binary audio frames ([1B type][4B BE header len][header JSON][mp3]),
// serializes tracks, schedules chunks on the AudioContext timeline and paints
// karaoke against the audio clock. Used by the OBS overlay and the try pages;
// sockets and page chrome stay outside.

window.OverlayPlayer = class OverlayPlayer {
    constructor(opts) {
        this.audioContext = opts.audioContext;
        this.cardEl = opts.cardEl;
        this.windowEl = opts.windowEl;
        this.textEl = opts.textEl;
        this.onTrackFinished = opts.onTrackFinished || null;
        this.onTrackActivated = opts.onTrackActivated || null;

        this.masterGain = this.audioContext.createGain();
        this.analyser = this.audioContext.createAnalyser();
        this.analyser.fftSize = 512;
        this.masterGain.connect(this.analyser);
        this.analyser.connect(this.audioContext.destination);
        this.analyserData = new Uint8Array(this.analyser.fftSize);

        this.pendingSkips = new Set(); // msg_id, never pruned (reset clears)
        this.tracks = new Map();
        this.playQueue = [];
        this.activeTrackId = null;
        this.generation = 0;
        this.lastActiveEl = null;

        this._alive = true;
        // line wrapping changes on resize, so the karaoke window must re-scroll
        // to the active word at its new position
        this._onResize = () => {
            if (this.lastActiveEl) this._scrollTo(this.lastActiveEl);
        };
        window.addEventListener('resize', this._onResize);

        const loop = () => {
            if (!this._alive) return;
            this._renderFrame();
            requestAnimationFrame(loop);
        };
        requestAnimationFrame(loop);
    }

    destroy() {
        this._alive = false;
        window.removeEventListener('resize', this._onResize);
        this.reset();
        try { this.analyser.disconnect(); } catch (e) { }
        try { this.masterGain.disconnect(); } catch (e) { }
    }

    // ---- public API ----

    // returns true when the buffer was an overlay-v2 frame (first byte 1/2/3)
    handleFrame(buf) {
        const view = new DataView(buf);
        const frameType = view.getUint8(0);
        if (frameType === 3) return true; // ping
        if (frameType !== 1 && frameType !== 2) return false;

        const headerLen = view.getUint32(1, false);
        const header = JSON.parse(new TextDecoder('utf-8').decode(new Uint8Array(buf, 5, headerLen)));

        if (frameType === 2) { // track_done
            // a done frame for a skipped message must not resurrect the track
            if (this.pendingSkips.has(header.msg_id)) return true;
            // the bus does not guarantee cross-frame order: done can overtake
            // the first chunk, so it must create the track rather than drop
            const track = this._ensureTrack(header.track_id, header.msg_id);
            track.done = true;
            track.totalDurMs = header.total_dur_ms || 0;
            return true;
        }

        if (this.pendingSkips.has(header.msg_id)) return true;

        const chunk = {
            offsetMs: header.offset_ms || 0,
            durMs: header.dur_ms || 0,
            words: header.words || [],
            mp3: buf.slice(5 + headerLen),
        };

        const track = this._ensureTrack(header.track_id, header.msg_id);

        if (this.activeTrackId === track.id) {
            this._scheduleChunk(track, chunk);
        } else {
            track.chunks.push(chunk);
            if (!track.queued) {
                track.queued = true;
                this.playQueue.push(track.id);
            }
        }

        this._tryActivate();
        return true;
    }

    registerMeta(trackId, msgId) {
        if (!this.pendingSkips.has(msgId)) {
            this._ensureTrack(trackId, msgId);
        }
    }

    addPendingSkips(ids) {
        for (const id of ids || []) this.pendingSkips.add(id);
    }

    skip(msgId, wipe) {
        this.pendingSkips.add(msgId);
        let killedActive = false;

        for (const [id, track] of this.tracks) {
            if (track.msgId !== msgId) continue;
            for (const src of track.sources) {
                try { src.stop(); } catch (e) { }
            }
            this.tracks.delete(id);
            const qi = this.playQueue.indexOf(id);
            if (qi >= 0) this.playQueue.splice(qi, 1);
            if (this.activeTrackId === id) {
                this.activeTrackId = null;
                killedActive = true;
            }
        }

        if (killedActive || wipe) {
            this.clearCaption();
        }
        this._tryActivate();
        return killedActive || wipe;
    }

    clearCaption() {
        this.textEl.innerHTML = '';
        this.textEl.style.transform = 'translateY(0)';
        this.cardEl.classList.remove('visible');
        this.lastActiveEl = null;
    }

    reset() {
        this.generation++;
        for (const [, track] of this.tracks) {
            for (const src of track.sources) {
                try { src.stop(); } catch (e) { }
            }
        }
        this.tracks.clear();
        this.playQueue.length = 0;
        this.activeTrackId = null;
        this.pendingSkips.clear();
        this.clearCaption();
    }

    activeMsgId() {
        const track = this.tracks.get(this.activeTrackId);
        return track ? track.msgId : null;
    }

    isPlaying() {
        return this.activeTrackId !== null;
    }

    // output RMS in [0..~1], for character voice pulse
    level() {
        this.analyser.getByteTimeDomainData(this.analyserData);
        let sum = 0;
        for (let i = 0; i < this.analyserData.length; i++) {
            const v = (this.analyserData[i] - 128) / 128;
            sum += v * v;
        }
        return Math.sqrt(sum / this.analyserData.length);
    }

    // ---- internals ----

    _ensureTrack(trackId, msgId) {
        let t = this.tracks.get(trackId);
        if (!t) {
            t = {
                id: trackId,
                msgId: msgId,
                chunks: [],
                sources: [],
                words: [],
                done: false,
                queued: false,
                totalDurMs: 0,
                maxChunkEndMs: 0,
                anchor: 0,
                nextStartAt: 0,
                decodeChain: Promise.resolve(),
            };
            this.tracks.set(trackId, t);
        }
        return t;
    }

    _trackTimeMs(track) {
        return (this.audioContext.currentTime - track.anchor) * 1000;
    }

    _scheduleChunk(track, chunk) {
        const gen = this.generation;
        // decodes are chained so chunks schedule in arrival order —
        // decodeAudioData callbacks are not guaranteed to complete in order
        track.decodeChain = track.decodeChain.then(() =>
            this.audioContext.decodeAudioData(chunk.mp3).then((buffer) => {
                if (gen !== this.generation) return;
                if (this.pendingSkips.has(track.msgId)) return;
                if (this.tracks.get(track.id) !== track) return;

                const source = this.audioContext.createBufferSource();
                source.buffer = buffer;
                source.connect(this.masterGain);

                // a chunk that arrives or decodes late slips the whole track:
                // re-anchor so later chunks and the karaoke clock follow,
                // otherwise the next chunk lands under this one's tail
                const scheduled = track.anchor + chunk.offsetMs / 1000;
                const startAt = Math.max(scheduled, track.nextStartAt, this.audioContext.currentTime);
                track.anchor += startAt - scheduled;
                track.nextStartAt = startAt + chunk.durMs / 1000;
                source.start(startAt);
                track.sources.push(source);
            }, (err) => {
                console.error('chunk decode failed', err);
            })
        );

        track.maxChunkEndMs = Math.max(track.maxChunkEndMs, chunk.offsetMs + chunk.durMs);

        for (const w of chunk.words) {
            const el = document.createElement('span');
            el.className = 'w';
            el.textContent = w.w + ' ';
            this.textEl.appendChild(el);
            track.words.push({ s: w.s, e: w.e, el: el });
        }
        this.cardEl.classList.add('visible');
    }

    _activateTrack(trackId) {
        const track = this.tracks.get(trackId);
        if (!track) return;

        this.activeTrackId = trackId;
        this.clearCaption();
        track.words = [];

        // anchor so the first buffered chunk plays immediately and the karaoke
        // clock stays correct when joining mid-track (offset > 0)
        const firstOffset = track.chunks.length ? track.chunks[0].offsetMs : 0;
        track.anchor = this.audioContext.currentTime + 0.05 - firstOffset / 1000;

        for (const chunk of track.chunks) {
            this._scheduleChunk(track, chunk);
        }
        track.chunks = [];
        if (this.onTrackActivated) this.onTrackActivated(track.msgId);
    }

    _tryActivate() {
        while (this.activeTrackId === null && this.playQueue.length > 0) {
            const next = this.playQueue.shift();
            if (!this.tracks.has(next)) continue;
            this._activateTrack(next);
        }
    }

    _finishTrack(track) {
        // caption stays on screen until the next track or a clean/skip
        for (const w of track.words) {
            w.el.classList.remove('active');
            w.el.classList.add('spoken');
        }
        // the clock says the track is over; make sure no scheduled tail can
        // bleed under the next track
        for (const src of track.sources) {
            try { src.stop(); } catch (e) { }
        }
        this.tracks.delete(track.id);
        if (this.activeTrackId === track.id) {
            this.activeTrackId = null;
        }
        this._tryActivate();
        if (this.onTrackFinished) this.onTrackFinished(track.msgId);
    }

    // keep the active word inside the visible window: shift the text up so the
    // active line is the window's last visible line
    _scrollTo(el) {
        const lineHeight = el.offsetHeight || 1;
        const shift = Math.max(0, el.offsetTop + lineHeight - this.windowEl.clientHeight);
        this.textEl.style.transform = `translateY(${-shift}px)`;
    }

    _renderFrame() {
        const track = this.tracks.get(this.activeTrackId);
        if (!track) return;

        const t = this._trackTimeMs(track);
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

        if (activeEl && activeEl !== this.lastActiveEl) {
            this._scrollTo(activeEl);
            this.lastActiveEl = activeEl;
        }

        const endMs = track.done ? Math.max(track.totalDurMs, track.maxChunkEndMs) : Infinity;
        if (t >= endMs) {
            this._finishTrack(track);
        }
    }
};
