package api

import (
	"app/pkg/ctxstore"
	"net/http"
)

func settings(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/", http.StatusMovedPermanently)
}

func index(w http.ResponseWriter, r *http.Request) {
	_, ok := ctxstore.GetUser(r.Context())
	if !ok {
		_ = html.ExecuteTemplate(w, "page.html", &Page{
			Title:   "BAJ AI - Home",
			Content: getHtml("index.html", nil),
		})

		return
	}

	_ = html.ExecuteTemplate(w, "main.html", nil)
}
