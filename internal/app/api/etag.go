package api

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
)

// etagMiddleware buffers successful GET responses, stamps them with a strong
// content-hash ETag, and answers If-None-Match with 304 — one caching policy
// for every endpoint instead of per-handler Cache-Control headers.
// Hijacked (websocket) and flushed (proxy/stream) responses pass through raw.
func etagMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}

		ew := &etagWriter{rw: w, status: http.StatusOK}
		next.ServeHTTP(ew, r)
		ew.finish(r)
	})
}

type etagWriter struct {
	rw          http.ResponseWriter
	buf         bytes.Buffer
	status      int
	passthrough bool // bytes already sent raw (hijacked or flushed)
	headerSent  bool
}

func (ew *etagWriter) Header() http.Header {
	return ew.rw.Header()
}

func (ew *etagWriter) WriteHeader(status int) {
	if ew.passthrough {
		if !ew.headerSent {
			ew.headerSent = true
			ew.rw.WriteHeader(status)
		}
		return
	}
	ew.status = status
}

func (ew *etagWriter) Write(b []byte) (int, error) {
	if ew.passthrough {
		if !ew.headerSent {
			ew.headerSent = true
			ew.rw.WriteHeader(ew.status)
		}
		return ew.rw.Write(b)
	}
	return ew.buf.Write(b)
}

// Flush switches to passthrough: the handler is streaming, so buffering for a
// hash would break it (and reverse proxies flush periodically).
func (ew *etagWriter) Flush() {
	if !ew.passthrough {
		ew.passthrough = true
		if !ew.headerSent {
			ew.headerSent = true
			ew.rw.WriteHeader(ew.status)
		}
		if ew.buf.Len() > 0 {
			_, _ = ew.rw.Write(ew.buf.Bytes())
			ew.buf.Reset()
		}
	}
	if f, ok := ew.rw.(http.Flusher); ok {
		f.Flush()
	}
}

func (ew *etagWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := ew.rw.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
	}
	ew.passthrough = true
	ew.headerSent = true
	return h.Hijack()
}

func (ew *etagWriter) finish(r *http.Request) {
	if ew.passthrough {
		return
	}

	if ew.status != http.StatusOK || ew.buf.Len() == 0 {
		ew.rw.WriteHeader(ew.status)
		_, _ = ew.rw.Write(ew.buf.Bytes())
		return
	}

	sum := sha256.Sum256(ew.buf.Bytes())
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`

	if r.Header.Get("If-None-Match") == etag {
		ew.rw.Header().Set("ETag", etag)
		ew.rw.WriteHeader(http.StatusNotModified)
		return
	}

	ew.rw.Header().Set("ETag", etag)
	ew.rw.WriteHeader(http.StatusOK)
	_, _ = ew.rw.Write(ew.buf.Bytes())
}
