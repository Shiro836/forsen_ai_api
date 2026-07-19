package api

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestResizeToFit(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 2000, 1000))); err != nil {
		t.Fatal(err)
	}

	out, err := resizeToFit(buf.Bytes(), 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 500 || cfg.Height != 250 {
		t.Errorf("expected 500x250, got %dx%d", cfg.Width, cfg.Height)
	}

	same, err := resizeToFit(buf.Bytes(), 4000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(same, buf.Bytes()) {
		t.Error("image within bounds should pass through untouched")
	}
}

func TestParseAlbumIDs(t *testing.T) {
	ids, err := parseAlbumIDs("abcdefghij")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 2 || ids[0] != "abcde" || ids[1] != "fghij" {
		t.Fatalf("unexpected ids: %v", ids)
	}

	for _, bad := range []string{"", "abc", "abcdef", "abcd!", strings.Repeat("abcde", maxAlbumImages+1)} {
		if _, err := parseAlbumIDs(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestAlbumTemplates(t *testing.T) {
	if page := getString("album.html", nil); strings.Contains(page, "error") && !strings.Contains(page, "album_files") {
		t.Fatalf("album.html failed to render: %s", page)
	}

	sb := &strings.Builder{}
	err := html.ExecuteTemplate(sb, "album_preview.html", &albumPreviewData{
		Count:     2,
		FirstURL:  "https://forsen.fun/images/abcde",
		ImageURLs: []string{"/images/abcde", "/images/fghij"},
	})
	if err != nil {
		t.Fatalf("album_preview.html: %v", err)
	}
	if !strings.Contains(sb.String(), "/images/fghij") {
		t.Fatalf("album_preview.html missing image urls: %s", sb.String())
	}
}
