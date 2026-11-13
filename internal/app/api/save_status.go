package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// saveStatus is the text beside a Save button. Baseline fingerprints the form
// as it was last saved; the form reads as changed while its fingerprint differs.
type saveStatus struct {
	// ID must end in _result: that is what lets an error response be swapped in.
	ID       string
	Baseline string
	// Skip names the file fields, which would otherwise be uploaded on every check.
	Skip  string
	Check bool
	Saved bool
	Dirty bool
	Error string
}

var saveStatusID = regexp.MustCompile(`^[a-z0-9_]+_result$`)

// newSaveStatus is the status of a freshly rendered form: it has no baseline
// yet and asks for one as soon as it loads. skip is the optional Skip value.
func newSaveStatus(id string, skip ...string) saveStatus {
	return saveStatus{ID: id, Skip: strings.Join(skip, ","), Check: true}
}

// formFingerprint ignores "_" fields, the status's own bookkeeping, and the
// skipped ones: a save posts an empty file input as a plain empty value, which
// a check never sends.
func formFingerprint(form url.Values, skip string) string {
	skipped := strings.Split(skip, ",")

	keys := make([]string, 0, len(form))
	for key := range form {
		if !strings.HasPrefix(key, "_") && !slices.Contains(skipped, key) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)

	hash := sha256.New()
	for _, key := range keys {
		hash.Write([]byte(key))
		hash.Write([]byte{0})
		for _, value := range form[key] {
			hash.Write([]byte(value))
			hash.Write([]byte{0})
		}
		hash.Write([]byte{1})
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}

func parseAnyForm(r *http.Request) error {
	if err := r.ParseMultipartForm(1 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		return err
	}
	return r.ParseForm()
}

// formChanges answers whether the posted form still matches its baseline. A
// form without one gets its current state as the baseline.
func (api *API) formChanges(w http.ResponseWriter, r *http.Request) {
	if err := parseAnyForm(r); err != nil {
		http.Error(w, "failed to parse form: "+err.Error(), http.StatusBadRequest)
		return
	}

	// the target, not the trigger: a hash field asks on the status's behalf
	status := &saveStatus{
		ID:       r.Header.Get("HX-Target"),
		Baseline: r.Form.Get("_baseline"),
		Skip:     r.Form.Get("_skip"),
	}
	if !saveStatusID.MatchString(status.ID) {
		http.Error(w, "unknown form", http.StatusBadRequest)
		return
	}

	current := formFingerprint(r.Form, status.Skip)
	if status.Baseline == "" {
		status.Baseline = current
	}
	status.Dirty = current != status.Baseline
	// "saved" stays up for as long as the form is still what was saved
	status.Saved = !status.Dirty && r.Form.Get("_saved") == "1"

	_ = html.ExecuteTemplate(w, "save-status", status)
}

// fileHash stands in for a file input in the form's fingerprint: the file
// itself is never part of a check, its hash in a hidden field is.
type fileHash struct {
	Field string
	Hash  string
	// StatusID and Skip are those of the form's save status.
	StatusID string
	Skip     string
	// Check makes the field ask for the form's status as soon as it lands.
	// htmx wires listeners onto swapped-in content only after its settle delay,
	// so an event telling the status to look again can arrive at an element
	// that is not listening yet; the field's own load cannot be missed.
	Check bool
}

var (
	fileHashField = regexp.MustCompile(`^[a-z0-9_]+$`)
	fileHashSkip  = regexp.MustCompile(`^[a-z0-9_,]*$`)
)

func hashBytes(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

func newFileHash(statusID, skip, field string, data []byte) fileHash {
	return fileHash{Field: field, Hash: hashBytes(data), StatusID: statusID, Skip: skip}
}

// formFileHash takes the one file a picker just got and answers with its hash
// field; an emptied picker goes back to the hash of what is stored.
func (api *API) formFileHash(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(20 << 20); err != nil {
		http.Error(w, "failed to parse form: "+err.Error(), http.StatusBadRequest)
		return
	}

	hash := fileHash{
		Field:    r.Form.Get("_file_field"),
		Hash:     r.Form.Get("_file_original"),
		StatusID: r.Form.Get("_file_status"),
		Skip:     r.Form.Get("_file_skip"),
		Check:    true,
	}
	if !fileHashField.MatchString(hash.Field) || !saveStatusID.MatchString(hash.StatusID) || !fileHashSkip.MatchString(hash.Skip) {
		http.Error(w, "unknown field", http.StatusBadRequest)
		return
	}

	if file, _, err := r.FormFile(hash.Field); err == nil {
		data, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil {
			http.Error(w, "failed to read file: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(data) > 0 {
			hash.Hash = hashBytes(data)
		}
	}

	_ = html.ExecuteTemplate(w, "file-hash", hash)
}

// writeSaved answers a successful save: what was just posted is the new baseline.
func writeSaved(w http.ResponseWriter, r *http.Request, id, skip string) {
	_ = html.ExecuteTemplate(w, "save-status", &saveStatus{
		ID:       id,
		Skip:     skip,
		Baseline: formFingerprint(r.Form, skip),
		Saved:    true,
	})
}

// writeSaveError keeps the old baseline, so the form stays changed until a
// save goes through.
func writeSaveError(w http.ResponseWriter, r *http.Request, id, skip string, code int, message string) {
	w.WriteHeader(code)
	_ = html.ExecuteTemplate(w, "save-status", &saveStatus{
		ID:       id,
		Skip:     skip,
		Baseline: r.Form.Get("_baseline"),
		Error:    message,
	})
}
