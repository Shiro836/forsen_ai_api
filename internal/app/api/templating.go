package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"embed"
	"html/template"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jritsema/gotoolbox/web"
)

var (
	//go:embed all:static/*
	staticFS embed.FS

	//go:embed all:templates/*
	templateFS embed.FS

	//parsed templates
	html *template.Template
)

func init() {
	var err error
	html, err = web.TemplateParseFSRecursive(templateFS, ".html", true, nil)
	if err != nil {
		panic(err)
	}
}

func getString(templateName string, data any) string {
	sb := &strings.Builder{}
	err := html.ExecuteTemplate(sb, templateName, data)
	if err != nil {
		return err.Error()
	}

	return sb.String()
}

func getHtml(templateName string, data any) template.HTML {
	return template.HTML(getString(templateName, data))
}

type page struct {
	Title        string
	Content      template.HTML
	LogoutButton bool
	DarkTheme    bool
}

func isDarkTheme(r *http.Request) bool {
	themeCookie, err := r.Cookie("theme")
	if err != nil {
		return true
	}

	return themeCookie.Value == "dark"
}

func isLogout(r *http.Request) bool {
	return ctxstore.GetUser(r.Context()) != nil
}

type LoginPage struct {
	RedirectUrl string
}

func createPage(r *http.Request) *page {
	return &page{
		Title:        "BAJ AI",
		LogoutButton: isLogout(r),
		DarkTheme:    isDarkTheme(r),
	}
}

func errPage(r *http.Request, errCode int, errMessage string) *page {
	page := createPage(r)
	page.Title = "BAJ AI - Error"
	page.Content = getHtml("error.html", &htmlErr{
		ErrorCode:    errCode,
		ErrorMessage: errMessage,
	})

	return page
}

func submitPage(w http.ResponseWriter, page *page) {
	_ = html.ExecuteTemplate(w, "page.html", page)
}

type permission struct {
	PermissionID   int
	PermissionName string
}

type noPermissionsPage struct {
	Permissions []permission
}

type navPage struct {
	IsStreamer bool
	IsMod      bool
	IsAdmin    bool

	Content template.HTML
}

type permissionRequest struct {
	Login  string
	UserID uuid.UUID
}

type modPage struct {
	Requests  []permissionRequest
	Streamers []permissionRequest
}

type homePage struct {
	URL string
}

func empty(r *http.Request) template.HTML {
	return ""
}

func (api *API) elemNoPermissions(getContent func(r *http.Request) template.HTML) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(getContent(r)))
	})
}

func (api *API) elem(getContent func(r *http.Request) template.HTML) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := ctxstore.GetUser(r.Context())

		if user == nil {
			_, _ = w.Write([]byte("not authenticated bitch"))

			return
		}

		_, _ = w.Write([]byte(getContent(r)))
	})
}

// navPublic renders the standard page layout (styles and navbar) without enforcing authentication.
// If a user exists, it enriches the navbar with permissions; otherwise, it renders a basic layout.
func (api *API) navPublic(getContent func(r *http.Request) template.HTML) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := ctxstore.GetUser(r.Context())

		page := createPage(r)

		navPage := &navPage{
			Content: getContent(r),
		}

		if user != nil {
			userPermissions, err := api.db.GetUserPermissions(r.Context(), user.ID, db.PermissionStatusGranted)
			if err != nil {
				submitPage(w, errPage(r, http.StatusInternalServerError, err.Error()))

				return
			}

			for _, permission := range userPermissions {
				switch permission {
				case db.PermissionStreamer:
					navPage.IsStreamer = true
				case db.PermissionMod:
					navPage.IsMod = true
				case db.PermissionAdmin:
					navPage.IsAdmin = true
				}
			}
		}

		page.Content = getHtml("navbar.html", navPage)
		submitPage(w, page)
	})
}

func (api *API) nav(getContent func(r *http.Request) template.HTML) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := ctxstore.GetUser(r.Context())

		page := createPage(r)

		if user == nil {
			submitPage(w, authPage(r))

			return
		}

		userPermissions, err := api.db.GetUserPermissions(r.Context(), user.ID, db.PermissionStatusGranted)
		if err != nil {
			submitPage(w, errPage(r, http.StatusInternalServerError, err.Error()))

			return
		}

		navPage := &navPage{
			Content: getContent(r),
		}
		for _, permission := range userPermissions {
			switch permission {
			case db.PermissionStreamer:
				navPage.IsStreamer = true
			case db.PermissionMod:
				navPage.IsMod = true
			case db.PermissionAdmin:
				navPage.IsAdmin = true
			}
		}

		page.Content = getHtml("navbar.html", navPage)
		submitPage(w, page)
	})
}
