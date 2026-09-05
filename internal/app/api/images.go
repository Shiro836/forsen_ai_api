package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"

	"image"
	"image/gif"
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
	// 1024 on read (processor.imageForLLM), so this doesn't affect prompts.
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

	cfg, format, err := image.DecodeConfig(file)
	if err != nil {
		return "", http.StatusBadRequest, fmt.Errorf("invalid image: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxImagePixels {
		return "", http.StatusBadRequest, fmt.Errorf("image dimensions not allowed: %dx%d", cfg.Width, cfg.Height)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", http.StatusInternalServerError, fmt.Errorf("seek error: %w", err)
	}

	id, err := randomID(imageIDLength)
	if err != nil {
		return "", http.StatusInternalServerError, fmt.Errorf("id error: %w", err)
	}

	raw, err := io.ReadAll(file)
	if err != nil {
		return "", http.StatusBadRequest, fmt.Errorf("read upload: %w", err)
	}

	var stored []byte
	contentType := "image/png"
	if animated(raw, format) {
		// Go's decoders see one frame, so animations are stored as uploaded and
		// only scaled through ffmpeg when they exceed the share-link ceiling.
		stored = raw
		contentType = "image/" + format
		if cfg.Width > maxStoredDim || cfg.Height > maxStoredDim {
			stored, err = api.ffmpeg.FitWebP(r.Context(), raw, maxStoredDim, maxStoredDim, variantQuality)
			if err != nil {
				return "", http.StatusBadRequest, fmt.Errorf("invalid image: %w", err)
			}
			contentType = "image/webp"
		}
	} else {
		src, _, err := image.Decode(bytes.NewReader(raw))
		if err != nil {
			return "", http.StatusBadRequest, fmt.Errorf("invalid image: %w", err)
		}
		dst := imaging.Fit(src, maxStoredDim, maxStoredDim, imaging.Lanczos)
		var out bytes.Buffer
		if err := imgpng.Encode(&out, dst); err != nil {
			return "", http.StatusInternalServerError, fmt.Errorf("encode error: %w", err)
		}
		stored = out.Bytes()
	}

	if err := api.s3.PutObject(r.Context(), s3client.UserImagesBucket, id, bytes.NewReader(stored), int64(len(stored)), contentType); err != nil {
		return "", http.StatusInternalServerError, fmt.Errorf("upload error: %w", err)
	}

	return id, http.StatusOK, nil
}

// animated reports whether a gif has more than one frame or a webp carries
// the animation flag; other formats have no animation to keep.
func animated(data []byte, format string) bool {
	switch format {
	case "gif":
		g, err := gif.DecodeAll(bytes.NewReader(data))
		return err == nil && len(g.Image) > 1
	case "webp":
		return len(data) > 20 && string(data[12:16]) == "VP8X" && data[20]&0x02 != 0
	}
	return false
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

func (api *API) readUserImage(ctx context.Context, id string) ([]byte, error) {
	obj, err := api.s3.GetObject(ctx, s3client.UserImagesBucket, id)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", id, err)
	}
	defer obj.Close()
	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", id, err)
	}
	return data, nil
}

func (api *API) imageGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(chi.URLParam(r, "id"), ".png")
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("missing id"))
		return
	}

	// ?w=&h= serve the image fitted to that box, so the overlay and control
	// panel fetch at their rendered size instead of full stored resolution.
	if box, ok := requestedBox(r); ok {
		data, err := api.variants.Get(r.Context(), s3client.UserImagesBucket, id, box, func(ctx context.Context) ([]byte, error) {
			return api.readUserImage(ctx, id)
		})
		if err != nil {
			api.logger.Error("image variant", "id", id, "box", box, "error", err)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not found"))
			return
		}
		w.Header().Set("Content-Type", "image/webp")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
		return
	}

	data, err := api.readUserImage(r.Context(), id)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
		return
	}

	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}
