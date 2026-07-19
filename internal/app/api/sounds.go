package api

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"

	"app/internal/app/processor"

	"github.com/go-chi/chi/v5"
)

//go:embed sound_names.json
var soundNamesJSON []byte

type soundItem struct {
	ID   int
	Name string
}

// soundItems lists all embedded SFX sounds with their human-readable names, sorted by ID.
var soundItems []soundItem

func init() {
	var names map[string]string
	if err := json.Unmarshal(soundNamesJSON, &names); err != nil {
		panic("failed to parse sound_names.json: " + err.Error())
	}

	for idStr, name := range names {
		id, err := strconv.Atoi(idStr)
		if err != nil {
			continue
		}
		soundItems = append(soundItems, soundItem{ID: id, Name: name})
	}

	sort.Slice(soundItems, func(i, j int) bool { return soundItems[i].ID < soundItems[j].ID })
}

func (api *API) soundGet(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid sound id"))
		return
	}

	data, err := processor.GetSFX(strconv.Itoa(id))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("sound not found"))
		return
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	_, _ = w.Write(data)
}
