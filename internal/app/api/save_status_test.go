package api

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormFingerprint(t *testing.T) {
	form := url.Values{"name": {"a"}, "on": {"x", "y"}}
	same := url.Values{"on": {"x", "y"}, "name": {"a"}, "_baseline": {"whatever"}, "_saved": {"1"}}
	assert.Equal(t, formFingerprint(form, ""), formFingerprint(same, ""), "field order and bookkeeping fields do not count")

	assert.NotEqual(t, formFingerprint(form, ""), formFingerprint(url.Values{"name": {"b"}, "on": {"x", "y"}}, ""))
	assert.NotEqual(t, formFingerprint(form, ""), formFingerprint(url.Values{"name": {"a"}, "on": {"x"}}, ""))
	assert.NotEqual(t, formFingerprint(url.Values{"a": {"bc"}}, ""), formFingerprint(url.Values{"ab": {"c"}}, ""))

	withFile := url.Values{"name": {"a"}, "on": {"x", "y"}, "image": {""}}
	assert.Equal(t, formFingerprint(form, "voice_ref,image"), formFingerprint(withFile, "voice_ref,image"))
}

func postFormChanges(t *testing.T, id string, form url.Values) (int, string) {
	r := httptest.NewRequest(http.MethodPost, "/form/changes", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("HX-Target", id)
	w := httptest.NewRecorder()
	(&API{}).formChanges(w, r)
	return w.Code, w.Body.String()
}

func baselineOf(t *testing.T, body string) string {
	_, after, found := strings.Cut(body, `name="_baseline" value="`)
	require.True(t, found, body)
	baseline, _, _ := strings.Cut(after, `"`)
	require.NotEmpty(t, baseline)
	return baseline
}

func postFileHash(t *testing.T, original string, file []byte) (*httptest.ResponseRecorder, string) {
	body := &bytes.Buffer{}
	form := multipart.NewWriter(body)
	require.NoError(t, form.WriteField("_file_field", "image"))
	require.NoError(t, form.WriteField("_file_original", original))
	require.NoError(t, form.WriteField("_file_status", "character_save_result"))
	require.NoError(t, form.WriteField("_file_skip", "voice_ref,image"))
	if file != nil {
		part, err := form.CreateFormFile("image", "picked.png")
		require.NoError(t, err)
		_, _ = part.Write(file)
	}
	require.NoError(t, form.Close())

	r := httptest.NewRequest(http.MethodPost, "/form/file-hash", body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	w := httptest.NewRecorder()
	(&API{}).formFileHash(w, r)
	return w, w.Body.String()
}

func TestFormFileHash(t *testing.T) {
	stored := hashBytes([]byte("stored picture"))
	require.NotEmpty(t, stored)
	assert.Empty(t, hashBytes(nil), "no file stored yet")

	w, body := postFileHash(t, stored, []byte("another picture"))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, body, `id="image__hash"`)
	assert.Contains(t, body, `value="`+hashBytes([]byte("another picture"))+`"`)

	// An event sent to the status can land before htmx made it listen; the
	// arriving field has to ask by itself, and push aside any older answer.
	assert.Empty(t, w.Header().Get("HX-Trigger-After-Swap"))
	for _, want := range []string{
		`hx-post="/form/changes"`,
		`hx-trigger="load"`,
		`hx-target="#character_save_result"`,
		`hx-sync="#character_save_result:replace"`,
		`hx-params="not voice_ref,image"`,
	} {
		assert.Contains(t, body, want)
	}

	_, body = postFileHash(t, stored, []byte("stored picture"))
	assert.Contains(t, body, `value="`+stored+`"`, "picking the stored file again is no change")

	_, body = postFileHash(t, stored, nil)
	assert.Contains(t, body, `value="`+stored+`"`, "an emptied picker goes back to what is stored")
}

func TestCharacterPageCarriesFileHashes(t *testing.T) {
	image := newFileHash(characterSaveStatusID, characterSaveSkip, "image", []byte("image"))
	page := getString("character.html", &characterPage{
		MessageExamples: &msgExamples{},
		VoiceRefHash:    newFileHash(characterSaveStatusID, characterSaveSkip, "voice_ref", []byte("voice")),
		ImageHash:       image,
	})

	for _, want := range []string{
		`name="image__hash" value="` + image.Hash + `"`,
		`hx-params="image,_file_field,_file_original,_file_status,_file_skip"`,
		`"_file_original": "` + image.Hash + `", "_file_status": "character_save_result", "_file_skip": "voice_ref,image"`,
	} {
		assert.Truef(t, strings.Contains(page, want), "character page lacks %s", want)
	}

	_, hashField, _ := strings.Cut(page, `id="image__hash"`)
	hashField, _, _ = strings.Cut(hashField, ">")
	assert.NotContains(t, hashField, "hx-post", "the rendered field must not check on load: the status already does")
}

func TestFormChanges(t *testing.T) {
	code, body := postFormChanges(t, "settings_save_result", url.Values{"name": {"a"}, "_baseline": {""}})
	require.Equal(t, http.StatusOK, code)
	assert.NotContains(t, body, "unsaved changes")
	assert.NotContains(t, body, "load,", "an answer must not ask again by itself")
	baseline := baselineOf(t, body)

	_, body = postFormChanges(t, "settings_save_result", url.Values{"name": {"b"}, "_baseline": {baseline}})
	assert.Contains(t, body, "unsaved changes")
	assert.Equal(t, baseline, baselineOf(t, body), "a changed form keeps its baseline")

	_, body = postFormChanges(t, "settings_save_result", url.Values{"name": {"a"}, "_baseline": {baseline}, "_saved": {"1"}})
	assert.NotContains(t, body, "unsaved changes")
	assert.Contains(t, body, ">saved<", "saved stays while the form is what was saved")

	_, body = postFormChanges(t, "settings_save_result", url.Values{"name": {"b"}, "_baseline": {baseline}, "_saved": {"1"}})
	assert.NotContains(t, body, ">saved<")

	code, _ = postFormChanges(t, "evil\"><script>", url.Values{"name": {"a"}})
	assert.Equal(t, http.StatusBadRequest, code)
}
