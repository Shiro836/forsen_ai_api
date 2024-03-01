package api

import (
	"embed"
	"html/template"
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
	_ = html.ExecuteTemplate(sb, templateName, data)
	return sb.String()
}

func getHtml(templateName string, data any) template.HTML {
	return template.HTML(getString(templateName, data))
}

type Page struct {
	Title   string
	Content template.HTML
}
