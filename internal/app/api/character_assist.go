package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"app/db"
	"app/pkg/charassist"
	"app/pkg/ctxstore"
)

const (
	assistMaxBodyBytes      = 256 << 10
	assistRequestTimeout    = 90 * time.Second
	assistDefaultExamplesCt = 3
)

// CharacterAssistant generates and refines character card text fields.
type CharacterAssistant interface {
	Generate(ctx context.Context, prompt string, current charassist.Draft) (*charassist.Draft, error)
	GenerateExamples(ctx context.Context, current charassist.Draft, count int) ([]db.MessageExample, error)
}

type assistCharacterRequest struct {
	Prompt string `json:"prompt"`
	charassist.Draft
}

type assistCharacterResponse struct {
	charassist.Draft
	ExamplesHTML string `json:"examples_html"`
}

type assistExamplesResponse struct {
	Added           int                 `json:"added"`
	MessageExamples []db.MessageExample `json:"message_examples"`
	ExamplesHTML    string              `json:"examples_html"`
}

func (api *API) assistCharacter(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		writeAssistError(w, http.StatusUnauthorized, "not authorized")
		return
	}
	if api.charAssist == nil {
		writeAssistError(w, http.StatusServiceUnavailable, "AI assistant is unavailable")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, assistMaxBodyBytes)
	var req assistCharacterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAssistError(w, http.StatusBadRequest, "invalid request")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), assistRequestTimeout)
	defer cancel()

	draft, err := api.charAssist.Generate(ctx, req.Prompt, req.Draft)
	if err != nil {
		if errors.Is(err, charassist.ErrEmptyInput) {
			writeAssistError(w, http.StatusBadRequest, err.Error())
			return
		}
		api.logger.Error("character assist generate failed", "user", user.TwitchLogin, "err", err)
		writeAssistError(w, http.StatusBadGateway, "failed to generate character; please try again")
		return
	}

	writeAssistJSON(w, http.StatusOK, assistCharacterResponse{
		Draft:        *draft,
		ExamplesHTML: renderExamplesHTML(draft.MessageExamples),
	})
}

func (api *API) assistCharacterExamples(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		writeAssistError(w, http.StatusUnauthorized, "not authorized")
		return
	}
	if api.charAssist == nil {
		writeAssistError(w, http.StatusServiceUnavailable, "AI assistant is unavailable")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, assistMaxBodyBytes)
	var req assistCharacterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAssistError(w, http.StatusBadRequest, "invalid request")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), assistRequestTimeout)
	defer cancel()

	clean := req.Draft.Clean()
	added, err := api.charAssist.GenerateExamples(ctx, clean, assistDefaultExamplesCt)
	if err != nil {
		if errors.Is(err, charassist.ErrEmptyInput) {
			writeAssistError(w, http.StatusBadRequest, "add a name or description before generating examples")
			return
		}
		api.logger.Error("character assist examples failed", "user", user.TwitchLogin, "err", err)
		writeAssistError(w, http.StatusBadGateway, "failed to generate examples; please try again")
		return
	}

	combined := make([]db.MessageExample, 0, len(clean.MessageExamples)+len(added))
	combined = append(combined, clean.MessageExamples...)
	combined = append(combined, added...)

	writeAssistJSON(w, http.StatusOK, assistExamplesResponse{
		Added:           len(added),
		MessageExamples: combined,
		ExamplesHTML:    renderExamplesHTML(combined),
	})
}

func renderExamplesHTML(examples []db.MessageExample) string {
	data := &msgExamples{ID: 0}
	if len(examples) == 0 {
		data.MessageExamples = []msgExample{{ID: 0}}
	} else {
		data.MessageExamples = make([]msgExample, len(examples))
		for i, ex := range examples {
			data.MessageExamples[i] = msgExample{
				ID:       i,
				Request:  ex.Request,
				Response: ex.Response,
			}
		}
		data.ID = len(examples) - 1
	}
	return getString("message_example.html", data)
}

func writeAssistJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAssistError(w http.ResponseWriter, code int, msg string) {
	writeAssistJSON(w, code, map[string]string{"error": msg})
}
