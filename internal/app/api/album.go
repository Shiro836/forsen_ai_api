package api

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"app/pkg/s3client"

	"github.com/go-chi/chi/v5"
)

// Album links are stateless: the URL path is the concatenation of fixed-length
// image IDs, so an album needs no record in the DB or S3 — the link itself is
// the album.

const maxAlbumImages = 100

func (api *API) albumPage(_ *http.Request) template.HTML {
	return getHtml("album.html", nil)
}

func (api *API) albumImageUpload(w http.ResponseWriter, r *http.Request) {
	id, status, err := api.storeImage(r)

	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
}

func parseAlbumIDs(s string) ([]string, error) {
	if len(s) == 0 || len(s)%imageIDLength != 0 {
		return nil, fmt.Errorf("malformed album id")
	}
	if len(s)/imageIDLength > maxAlbumImages {
		return nil, fmt.Errorf("too many images")
	}

	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(alphabet, rune(s[i])) {
			return nil, fmt.Errorf("malformed album id")
		}
	}

	ids := make([]string, 0, len(s)/imageIDLength)
	for i := 0; i < len(s); i += imageIDLength {
		ids = append(ids, s[i:i+imageIDLength])
	}

	return ids, nil
}

type albumPreviewData struct {
	Count     int
	FirstURL  string
	ImageURLs []string
}

func (api *API) albumPreview(w http.ResponseWriter, r *http.Request) {
	ids, err := parseAlbumIDs(chi.URLParam(r, "ids"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(err.Error()))
		return
	}

	if _, err := api.s3.StatObject(r.Context(), s3client.UserImagesBucket, ids[0]); err != nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
		return
	}

	urls := make([]string, 0, len(ids))
	for _, id := range ids {
		urls = append(urls, fmt.Sprintf("/images/%s", id))
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = html.ExecuteTemplate(w, "album_preview.html", &albumPreviewData{
		Count:     len(ids),
		FirstURL:  fmt.Sprintf("https://forsen.fun/images/%s", ids[0]),
		ImageURLs: urls,
	})
}
