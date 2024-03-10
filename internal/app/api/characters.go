package api

import (
	"html/template"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

func (api *API) characters(r *http.Request) template.HTML {
	return getHtml("characters.html", nil)
}

type characterPage struct {
	CharacterID int
}

func (api *API) character(r *http.Request) template.HTML {
	characterIDStr := chi.URLParam(r, "character_id")
	characterID, err := strconv.Atoi(characterIDStr)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "character_id is not a number",
		})
	}

	return getHtml("character.html", &characterPage{
		CharacterID: characterID,
	})
}

func (api *API) newCharacter(r *http.Request) template.HTML {
	return getHtml("error.html", &htmlErr{
		ErrorCode:    http.StatusInternalServerError,
		ErrorMessage: "not implemented!!!",
	})
}
