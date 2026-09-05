package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
}

func (s *fakeStore) GetObject(_ context.Context, bucket, name string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[bucket+"/"+name]
	if !ok {
		return nil, errors.New("no such key")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *fakeStore) PutObject(_ context.Context, bucket, name string, r io.Reader, _ int64, _ string) error {
	data, _ := io.ReadAll(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[bucket+"/"+name] = data
	s.puts++
	return nil
}

type fakeEncoder struct {
	calls atomic.Int32
	delay time.Duration
}

func (e *fakeEncoder) FitWebP(_ context.Context, data []byte, maxW, maxH, _ int) ([]byte, error) {
	e.calls.Add(1)
	time.Sleep(e.delay)
	return []byte(fmt.Sprintf("webp%dx%d:%s", maxW, maxH, data)), nil
}

func TestSnapDim(t *testing.T) {
	cases := map[int]int{1: 200, 200: 200, 201: 300, 336: 400, 420: 600, 756: 800, 1512: 1600, 4000: 3840}
	for in, want := range cases {
		if got, ok := snapDim(in); !ok || got != want {
			t.Errorf("snapDim(%d) = %d,%v want %d", in, got, ok, want)
		}
	}
	if _, ok := snapDim(0); ok {
		t.Error("0 must be rejected")
	}
}

func TestRequestedBox(t *testing.T) {
	cases := map[string]fitBox{
		"w=806&h=756": {1200, 800},
		"w=336&h=420": {400, 600},
		"w=200":       {200, 200},
		"h=756":       {800, 800},
		"w=99999":     {3840, 3840},
	}
	for q, want := range cases {
		box, ok := requestedBox(httptest.NewRequest(http.MethodGet, "/x?"+q, nil))
		if !ok || box != want {
			t.Errorf("%s: got %+v,%v want %+v", q, box, ok, want)
		}
	}
	for _, q := range []string{"", "w=0", "w=abc", "h=-5"} {
		if _, ok := requestedBox(httptest.NewRequest(http.MethodGet, "/x?"+q, nil)); ok {
			t.Errorf("%q must be rejected", q)
		}
	}
}

func TestVariantCacheEncodesOnceAndPersists(t *testing.T) {
	store := &fakeStore{objects: map[string][]byte{}}
	enc := &fakeEncoder{}
	cache := NewVariantCache(store, enc, slog.Default())
	source := func(context.Context) ([]byte, error) { return []byte("orig"), nil }

	got, err := cache.Get(context.Background(), "b", "img1", fitBox{800, 800}, source)
	if err != nil || string(got) != "webp800x800:orig" {
		t.Fatalf("first get: %q %v", got, err)
	}
	if enc.calls.Load() != 1 || store.puts != 1 || store.objects["b/"+variantKey("img1", fitBox{800, 800})] == nil {
		t.Fatalf("encode=%d puts=%d objects=%v", enc.calls.Load(), store.puts, store.objects)
	}

	if _, err := cache.Get(context.Background(), "b", "img1", fitBox{800, 800}, source); err != nil || enc.calls.Load() != 1 {
		t.Fatalf("second get should hit memory: encodes=%d err=%v", enc.calls.Load(), err)
	}

	if _, err := cache.Get(context.Background(), "b", "img1", fitBox{1600, 800}, source); err != nil || enc.calls.Load() != 2 {
		t.Fatalf("another box is another variant: encodes=%d err=%v", enc.calls.Load(), err)
	}

	fresh := NewVariantCache(store, enc, slog.Default())
	got, err = fresh.Get(context.Background(), "b", "img1", fitBox{800, 800}, source)
	if err != nil || string(got) != "webp800x800:orig" || enc.calls.Load() != 2 {
		t.Fatalf("a new process must reuse the S3 copy: %q encodes=%d err=%v", got, enc.calls.Load(), err)
	}
}

func TestVariantCacheCoalescesConcurrentEncodes(t *testing.T) {
	store := &fakeStore{objects: map[string][]byte{}}
	enc := &fakeEncoder{delay: 50 * time.Millisecond}
	cache := NewVariantCache(store, enc, slog.Default())
	source := func(context.Context) ([]byte, error) { return []byte("orig"), nil }

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := cache.Get(ctx, "b", "img1", fitBox{800, 800}, source); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if enc.calls.Load() != 1 {
		t.Fatalf("expected one encode, got %d", enc.calls.Load())
	}
}

func TestVariantCacheEmptySource(t *testing.T) {
	store := &fakeStore{objects: map[string][]byte{}}
	enc := &fakeEncoder{}
	cache := NewVariantCache(store, enc, slog.Default())
	got, err := cache.Get(context.Background(), "b", "img1", fitBox{800, 800}, func(context.Context) ([]byte, error) { return nil, nil })
	if err != nil || got != nil || enc.calls.Load() != 0 {
		t.Fatalf("got %q err=%v encodes=%d", got, err, enc.calls.Load())
	}
}
