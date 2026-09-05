package api

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strconv"
)

// responseMiddleware buffers successful GET responses and applies one policy to
// all of them: a strong content-hash ETag answered with 304 on If-None-Match,
// and gzip for text bodies when the client accepts it.
// Hijacked (websocket) and flushed (proxy/stream) responses pass through raw.
func responseMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}

		ew := &bufferedWriter{rw: w, status: http.StatusOK}
		next.ServeHTTP(ew, r)
		ew.finish(r)
	})
}

type bufferedWriter struct {
	rw          http.ResponseWriter
	buf         bytes.Buffer
	status      int
	passthrough bool // bytes already sent raw (hijacked or flushed)
	headerSent  bool
}

func (ew *bufferedWriter) Header() http.Header {
	return ew.rw.Header()
}

func (ew *bufferedWriter) WriteHeader(status int) {
	if ew.passthrough {
		if !ew.headerSent {
			ew.headerSent = true
			ew.rw.WriteHeader(status)
		}
		return
	}
	ew.status = status
}

func (ew *bufferedWriter) Write(b []byte) (int, error) {
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
func (ew *bufferedWriter) Flush() {
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

func (ew *bufferedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := ew.rw.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
	}
	ew.passthrough = true
	ew.headerSent = true
	return h.Hijack()
}

func (ew *bufferedWriter) finish(r *http.Request) {
	if ew.passthrough {
		return
	}

	if ew.status != http.StatusOK || ew.buf.Len() == 0 {
		ew.rw.WriteHeader(ew.status)
		_, _ = ew.rw.Write(ew.buf.Bytes())
		return
	}

	h := ew.rw.Header()
	compress := compressible(r, h, ew.buf.Bytes())
	sum := sha256.Sum256(ew.buf.Bytes())
	etag := `"` + hex.EncodeToString(sum[:16])
	if compress {
		etag += "-gz"
	}
	etag += `"`
	h.Set("ETag", etag)
	if compress {
		h.Add("Vary", "Accept-Encoding")
	}

	if r.Header.Get("If-None-Match") == etag {
		ew.rw.WriteHeader(http.StatusNotModified)
		return
	}

	if !compress {
		ew.rw.WriteHeader(http.StatusOK)
		_, _ = ew.rw.Write(ew.buf.Bytes())
		return
	}

	body := gzipBytes(ew.buf.Bytes())
	h.Set("Content-Encoding", "gzip")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	// FileServer advertises ranges over the identity body; a range into the
	// gzipped one would be garbage.
	h.Del("Accept-Ranges")
	ew.rw.WriteHeader(http.StatusOK)
	_, _ = ew.rw.Write(body)
}
