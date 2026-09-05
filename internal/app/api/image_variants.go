package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	variantQuality = 90

	// variantEncodeTimeout bounds one ffmpeg run; a 380-frame gif at native
	// size measured at ~8 s.
	variantEncodeTimeout = 2 * time.Minute
)

// variantRungs is the ladder a requested dimension snaps up to, so a source
// has a bounded set of variants instead of one per client resolution. The
// lower rungs bracket the guide's 800x600 browser source (character box
// 336x420, prompt slots ~400x370); the top rung matches the largest stored
// user image.
var variantRungs = []int{200, 300, 400, 600, 800, 1200, 1600, 2400, 3840}

// snapDim returns the smallest ladder rung that is at least n; false for
// sizes that make no sense.
func snapDim(n int) (int, bool) {
	if n <= 0 {
		return 0, false
	}
	for _, rung := range variantRungs {
		if n <= rung {
			return rung, true
		}
	}
	return variantRungs[len(variantRungs)-1], true
}

// fitBox is the (already snapped) box a variant must fit within.
type fitBox struct {
	W, H int
}

func variantKey(sourceID string, box fitBox) string {
	return fmt.Sprintf("thumb/%s_w%d_h%d_q%d.webp", sourceID, box.W, box.H, variantQuality)
}

type variantStore interface {
	GetObject(ctx context.Context, bucket, name string) (io.ReadCloser, error)
	PutObject(ctx context.Context, bucket, name string, r io.Reader, size int64, contentType string) error
}

type variantEncoder interface {
	FitWebP(ctx context.Context, data []byte, maxW, maxH, quality int) ([]byte, error)
}

// VariantCache serves resized WebP renditions of stored images: memory first,
// then the S3 object derived from the source id and width, and only then an
// ffmpeg encode whose result is persisted. Source ids are immutable (a new
// upload gets a new id), so a variant can never go stale.
type VariantCache struct {
	store   variantStore
	encoder variantEncoder
	logger  *slog.Logger

	mu    sync.RWMutex
	items map[string][]byte
	sf    singleflight.Group
}

func NewVariantCache(store variantStore, encoder variantEncoder, logger *slog.Logger) *VariantCache {
	return &VariantCache{
		store:   store,
		encoder: encoder,
		logger:  logger,
		items:   make(map[string][]byte),
	}
}

// Get returns the variant of the image sourceID in bucket fitted to box.
// source loads the original bytes on a miss; an empty original yields nil.
func (c *VariantCache) Get(ctx context.Context, bucket, sourceID string, box fitBox, source func(context.Context) ([]byte, error)) ([]byte, error) {
	objKey := variantKey(sourceID, box)
	memKey := bucket + "/" + objKey

	c.mu.RLock()
	data, ok := c.items[memKey]
	c.mu.RUnlock()
	if ok {
		return data, nil
	}

	// The encode is shared by every waiter, so it must outlive the first
	// request that triggered it.
	v, err, _ := c.sf.Do(memKey, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), variantEncodeTimeout)
		defer cancel()
		data, err := c.load(ctx, bucket, objKey, box, source)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.items[memKey] = data
		c.mu.Unlock()
		return data, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

func (c *VariantCache) load(ctx context.Context, bucket, objKey string, box fitBox, source func(context.Context) ([]byte, error)) ([]byte, error) {
	if data, ok := c.fetch(ctx, bucket, objKey); ok {
		return data, nil
	}

	original, err := source(ctx)
	if err != nil {
		return nil, err
	}
	if len(original) == 0 {
		return nil, nil
	}

	data, err := c.encoder.FitWebP(ctx, original, box.W, box.H, variantQuality)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", objKey, err)
	}
	if err := c.store.PutObject(ctx, bucket, objKey, bytes.NewReader(data), int64(len(data)), "image/webp"); err != nil {
		// Served from memory regardless; the next process restart re-encodes.
		c.logger.Error("failed to persist image variant", "bucket", bucket, "key", objKey, "error", err)
	}
	return data, nil
}

// fetch reports false for a missing object; minio only surfaces that on read.
func (c *VariantCache) fetch(ctx context.Context, bucket, objKey string) ([]byte, bool) {
	obj, err := c.store.GetObject(ctx, bucket, objKey)
	if err != nil {
		return nil, false
	}
	defer obj.Close()
	data, err := io.ReadAll(obj)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}
