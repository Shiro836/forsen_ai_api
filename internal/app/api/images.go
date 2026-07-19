package api

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strconv"
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

	// maxStoredDim keeps share links at up to 4K; the LLM path downscales to
	// 1024 on read (processor.downscaleForLLM), so this doesn't affect prompts.
	maxStoredDim = 3840

	// maxImagePixels bounds decode memory on this public endpoint (~256MB
	// RGBA at 64MP); the multipart limit alone doesn't stop decompression
	// bombs — a small PNG can decode to gigabytes.
	maxImagePixels = 64 << 20
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

// storeImage parses the multipart "file" field, decodes, resizes, and uploads
// it to S3, returning the generated image ID (or an http status and error).
func (api *API) storeImage(r *http.Request) (string, int, error) {
	if err := r.ParseMultipartForm(50 << 20); err != nil { // 50MB, 4K sources can be large
		return "", http.StatusBadRequest, fmt.Errorf("invalid form: %w", err)
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		return "", http.StatusBadRequest, fmt.Errorf("missing file: %w", err)
	}
	defer file.Close()

	cfg, _, err := image.DecodeConfig(file)
	if err != nil {
		return "", http.StatusBadRequest, fmt.Errorf("invalid image: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxImagePixels {
		return "", http.StatusBadRequest, fmt.Errorf("image dimensions not allowed: %dx%d", cfg.Width, cfg.Height)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", http.StatusInternalServerError, fmt.Errorf("seek error: %w", err)
	}

	src, _, err := image.Decode(file)
	if err != nil {
		return "", http.StatusBadRequest, fmt.Errorf("invalid image: %w", err)
	}

	id, err := randomID(imageIDLength)
	if err != nil {
		return "", http.StatusInternalServerError, fmt.Errorf("id error: %w", err)
	}

	dst := imaging.Fit(src, maxStoredDim, maxStoredDim, imaging.Lanczos)

	var out bytes.Buffer
	if err := imgpng.Encode(&out, dst); err != nil {
		return "", http.StatusInternalServerError, fmt.Errorf("encode error: %w", err)
	}

	if err := api.s3.PutObject(r.Context(), s3client.UserImagesBucket, id, bytes.NewReader(out.Bytes()), int64(out.Len()), "image/png"); err != nil {
		return "", http.StatusInternalServerError, fmt.Errorf("upload error: %w", err)
	}

	return id, http.StatusOK, nil
}

// uploadImage handles the core upload logic: parse, decode, resize, store.
// Returns the generated ID or writes an error response and returns "".
func (api *API) uploadImage(w http.ResponseWriter, r *http.Request) string {
	id, status, err := api.storeImage(r)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(getHtml("error.html", &htmlErr{ErrorCode: status, ErrorMessage: err.Error()})))
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

// resizeToFit returns data re-encoded to fit within maxDim, or the original
// bytes untouched if the image already fits.
func resizeToFit(data []byte, maxDim int) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if cfg.Width <= maxDim && cfg.Height <= maxDim {
		return data, nil
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	dst := imaging.Fit(src, maxDim, maxDim, imaging.Lanczos)

	var out bytes.Buffer
	if err := imgpng.Encode(&out, dst); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}

	return out.Bytes(), nil
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

	data, err := io.ReadAll(obj)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("read error"))
		return
	}

	// ?w=N serves the image downscaled to fit N px, so the overlay can fetch
	// at its actual rendered size instead of full stored resolution.
	if ws := r.URL.Query().Get("w"); ws != "" {
		if want, err := strconv.Atoi(ws); err == nil && want > 0 {
			resized, err := resizeToFit(data, want)
			if err != nil {
				// Full-res still renders, just wastes bandwidth.
				api.logger.Error("failed to resize image, serving original", "id", id, "w", want, "error", err)
			} else {
				data = resized
			}
		}
	}

	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(data)
}
