package api

import (
	"bytes"
	"compress/gzip"
	"mime"
	"net/http"
	"strings"
	"sync"
)

// gzipMinSize: below this the gzip header and the extra CPU outweigh the saving.
const gzipMinSize = 1024

// compressible reports whether a buffered 200 body should be gzipped for this
// request, sniffing and setting Content-Type when the handler left it unset.
func compressible(r *http.Request, h http.Header, body []byte) bool {
	if len(body) < gzipMinSize || h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" {
		return false
	}
	if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		return false
	}
	ct := h.Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(body)
		h.Set("Content-Type", ct)
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	switch {
	case strings.HasPrefix(mt, "text/"),
		mt == "application/javascript", mt == "application/json",
		mt == "application/xml", mt == "image/svg+xml":
		return true
	}
	return strings.HasSuffix(mt, "+json") || strings.HasSuffix(mt, "+xml")
}

var gzipPool = sync.Pool{
	New: func() any { return gzip.NewWriter(nil) },
}

func gzipBytes(b []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(b) / 4)
	zw := gzipPool.Get().(*gzip.Writer)
	zw.Reset(&out)
	_, _ = zw.Write(b)
	_ = zw.Close()
	gzipPool.Put(zw)
	return out.Bytes()
}
