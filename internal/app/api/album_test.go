package api

import (
	"strings"
	"testing"
)

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
