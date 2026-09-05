package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestGzipResponse(t *testing.T) {
	body := strings.Repeat("<div class=\"card\">hello world</div>\n", 200)
	handler := responseMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		_, _ = w.Write([]byte(body))
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	plainETag := rec.Header().Get("ETag")
	if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != body {
		t.Fatalf("identity: encoding=%q len=%d", rec.Header().Get("Content-Encoding"), rec.Body.Len())
	}

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	h := rec.Header()
	if h.Get("Content-Encoding") != "gzip" || h.Get("Vary") != "Accept-Encoding" || h.Get("Accept-Ranges") != "" {
		t.Fatalf("gzip headers: %v", h)
	}
	if h.Get("ETag") == plainETag || !strings.HasSuffix(h.Get("ETag"), `-gz"`) {
		t.Fatalf("gzip ETag must differ from identity: %q vs %q", h.Get("ETag"), plainETag)
	}
	if rec.Body.Len() >= len(body) || h.Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("compressed len=%d content-length=%q raw=%d", rec.Body.Len(), h.Get("Content-Length"), len(body))
	}
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if string(got) != body {
		t.Fatal("gzip body does not round-trip")
	}

	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("If-None-Match", h.Get("ETag"))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("gzip revalidation: code=%d body=%d", rec.Code, rec.Body.Len())
	}
}

func TestGzipSkips(t *testing.T) {
	cases := map[string]struct {
		ct   string
		body string
	}{
		"small text": {"text/plain", "tiny"},
		"image":      {"image/png", strings.Repeat("\x89PNG", 1000)},
		"audio":      {"audio/wav", strings.Repeat("RIFF", 1000)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			handler := responseMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.ct)
				_, _ = w.Write([]byte(tc.body))
			}))
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != tc.body {
				t.Fatalf("should not compress: encoding=%q", rec.Header().Get("Content-Encoding"))
			}
		})
	}
}

func BenchmarkGzipPage(b *testing.B) {
	body := []byte(strings.Repeat("<div class=\"hover-lift border-2 rounded w-60 h-60\"><button hx-get=\"/characters/0199f2b3-73f6-78a3-b3aa-ddb84b848502/try\" hx-select=\"#tab-content\">Try</button></div>\n", 1500))
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		gzipBytes(body)
	}
}
