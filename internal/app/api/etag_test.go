package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestETagMiddleware(t *testing.T) {
	handler := etagMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello world"))
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	etag := rec.Header().Get("ETag")
	if rec.Code != http.StatusOK || etag == "" || rec.Body.String() != "hello world" {
		t.Fatalf("first GET: code=%d etag=%q body=%q", rec.Code, etag, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("revalidation: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestETagMiddlewareNonOK(t *testing.T) {
	handler := etagMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusNotFound || rec.Header().Get("ETag") != "" || rec.Body.String() != "not found" {
		t.Fatalf("code=%d etag=%q body=%q", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
}

func TestETagMiddlewareSkipsNonGET(t *testing.T) {
	handler := etagMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("created"))
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	if rec.Header().Get("ETag") != "" || rec.Body.String() != "created" {
		t.Fatalf("POST should pass through: etag=%q body=%q", rec.Header().Get("ETag"), rec.Body.String())
	}
}

func TestETagMiddlewareFlushPassthrough(t *testing.T) {
	handler := etagMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("chunk1"))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("chunk2"))
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Header().Get("ETag") != "" || rec.Body.String() != "chunk1chunk2" {
		t.Fatalf("flushed stream: etag=%q body=%q", rec.Header().Get("ETag"), rec.Body.String())
	}
	if !rec.Flushed {
		t.Fatal("flush was not forwarded")
	}
}
