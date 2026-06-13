package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/google/uuid"
)

// ImageProvider retrieves a character image by ID.
type ImageProvider interface {
	GetCharImage(ctx context.Context, cardID uuid.UUID) ([]byte, error)
}

// CachedImage holds pre-computed image data and its ETag.
type CachedImage struct {
	Data []byte
	ETag string
}

// ImageCache wraps an ImageProvider with an in-memory cache.
// Cache entries live forever until explicitly invalidated via Invalidate().
type ImageCache struct {
	provider ImageProvider
	mu       sync.RWMutex
	items    map[string]*CachedImage
}

func NewImageCache(provider ImageProvider) *ImageCache {
	return &ImageCache{
		provider: provider,
		items:    make(map[string]*CachedImage),
	}
}

// Get returns the cached image or fetches from the provider and caches it.
func (c *ImageCache) Get(ctx context.Context, cardID uuid.UUID) (*CachedImage, error) {
	key := cardID.String()

	c.mu.RLock()
	if cached, ok := c.items[key]; ok {
		c.mu.RUnlock()
		return cached, nil
	}
	c.mu.RUnlock()

	imgData, err := c.provider.GetCharImage(ctx, cardID)
	if err != nil {
		return nil, err
	}

	// Fallback image is handled by the caller, we just cache whatever we got
	hash := sha256.Sum256(imgData)
	etag := `"` + hex.EncodeToString(hash[:16]) + `"`

	cached := &CachedImage{Data: imgData, ETag: etag}

	c.mu.Lock()
	c.items[key] = cached
	c.mu.Unlock()

	return cached, nil
}

// Invalidate removes a specific character image from the cache.
func (c *ImageCache) Invalidate(cardID uuid.UUID) {
	c.mu.Lock()
	delete(c.items, cardID.String())
	c.mu.Unlock()
}

// InvalidateAll clears the entire cache.
func (c *ImageCache) InvalidateAll() {
	c.mu.Lock()
	c.items = make(map[string]*CachedImage)
	c.mu.Unlock()
}
