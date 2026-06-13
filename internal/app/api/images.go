package api

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"

	"image"
	_ "image/gif"
	_ "image/jpeg"

	// _ "image/png"
	imgpng "image/png"

	"github.com/disintegration/imaging"
	"github.com/go-chi/chi/v5"

	"app/pkg/s3client"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

const (
	imageIDLength = 5

	alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

type imagesResult struct {
	ID         string
	URL        string
	ImageURL   string
	ShowImgTag bool
}

func randomID(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	for i := 0; i < n; i++ {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b), nil
}

func (api *API) imagesPage(r *http.Request) template.HTML {
	return getHtml("images.html", nil)
}

// uploadImage handles the core upload logic: parse, decode, resize, store.
// Returns the generated ID or writes an error response and returns "".
func (api *API) uploadImage(w http.ResponseWriter, r *http.Request) string {
	if err := r.ParseMultipartForm(20 << 20); err != nil { // 20MB
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(getHtml("error.html", &htmlErr{ErrorCode: http.StatusBadRequest, ErrorMessage: "invalid form: " + err.Error()})))
		return ""
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(getHtml("error.html", &htmlErr{ErrorCode: http.StatusBadRequest, ErrorMessage: "missing file: " + err.Error()})))
		return ""
	}
	defer file.Close()

	src, _, err := image.Decode(file)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(getHtml("error.html", &htmlErr{ErrorCode: http.StatusBadRequest, ErrorMessage: "invalid image: " + err.Error()})))
		return ""
	}

	id, err := randomID(imageIDLength)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(getHtml("error.html", &htmlErr{ErrorCode: http.StatusInternalServerError, ErrorMessage: "id error: " + err.Error()})))
		return ""
	}

	dst := imaging.Fit(src, 1024, 1024, imaging.Lanczos)

	var out bytes.Buffer
	if err := imgpng.Encode(&out, dst); err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(getHtml("error.html", &htmlErr{ErrorCode: http.StatusInternalServerError, ErrorMessage: "encode error: " + err.Error()})))
		return ""
	}

	if err := api.s3.PutObject(r.Context(), s3client.UserImagesBucket, id, bytes.NewReader(out.Bytes()), int64(out.Len()), "image/png"); err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(getHtml("error.html", &htmlErr{ErrorCode: http.StatusInternalServerError, ErrorMessage: "upload error: " + err.Error()})))
		return ""
	}

	return id
}

func (api *API) imagesUpload(w http.ResponseWriter, r *http.Request) {
	id := api.uploadImage(w, r)
	if id == "" {
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(getHtml("images_result.html", &imagesResult{
		ID:         id,
		URL:        fmt.Sprintf("/i/%s", id),
		ImageURL:   fmt.Sprintf("/images/%s", id),
		ShowImgTag: r.URL.Query().Get("from") != "share",
	})))
}

func (api *API) sharePage(_ *http.Request) template.HTML {
	return getHtml("share.html", nil)
}

type imagePreviewData struct {
	ID       string
	ImageURL string
}

func (api *API) imagePreview(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("missing id"))
		return
	}

	_, err := api.s3.StatObject(r.Context(), s3client.UserImagesBucket, id)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = html.ExecuteTemplate(w, "image_preview.html", &imagePreviewData{
		ID:       id,
		ImageURL: fmt.Sprintf("https://forsen.fun/images/%s", id),
	})
}

func (api *API) imageGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(chi.URLParam(r, "id"), ".png")
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("missing id"))
		return
	}

	obj, err := api.s3.GetObject(r.Context(), s3client.UserImagesBucket, id)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
		return
	}
	defer obj.Close()

	// Try stat for content-type and ETag
	if info, err := api.s3.StatObject(r.Context(), s3client.UserImagesBucket, id); err == nil {
		if info.ContentType != "" {
			w.Header().Set("Content-Type", info.ContentType)
		}
		if info.ETag != "" {
			w.Header().Set("ETag", info.ETag)
			if match := r.Header.Get("If-None-Match"); match == info.ETag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}

	// Immutable content (ID never reused), cache aggressively
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")

	if _, err := io.Copy(w, obj); err != nil {
		return
	}
}
