package api

import (
	"app/pkg/ctxstore"
	"embed"
	"html/template"
	"net/http"
	"strings"

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
	user, ok := ctxstore.GetUser(r.Context())
	if !ok {
		return false
	}

	return user != nil
}

type LoginPage struct {
	RedirectUrl string
}

func createPage(r *http.Request) *page {
	return &page{
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
