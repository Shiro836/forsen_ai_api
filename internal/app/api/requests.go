package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"html/template"
	"net/http"
)

type PanelData struct {
	TwitchLogin  string
	TwitchUserID int
}

type controlPanel struct {
	Panels []PanelData
}

func (api *API) controlPanel(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "unauthorized",
		})
	}

	relations, err := api.db.GetRelations(r.Context(), user, db.RelationTypeModerating)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "failed to get relations: " + err.Error(),
		})
	}

	panels := make([]PanelData, 0, len(relations)+1)

	hasPerm, err := api.db.HasPermission(r.Context(), user.TwitchUserID, db.PermissionStreamer)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "failed to check permission: " + err.Error(),
		})
	}

	if hasPerm {
		panels = append(panels, PanelData{
			TwitchLogin:  user.TwitchLogin,
			TwitchUserID: user.TwitchUserID,
		})
	}

	for _, relation := range relations {
		panels = append(panels, PanelData{
			TwitchLogin:  relation.TwitchLogin2,
			TwitchUserID: relation.TwitchUserID2,
		})
	}

	return getHtml("control_panel.html", &controlPanel{
		Panels: panels,
	})
}
