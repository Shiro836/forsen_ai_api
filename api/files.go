package api

import (
	"app/db"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

func (api *API) UploadCharCardHandler(w http.ResponseWriter, r *http.Request) {
	charName := chi.URLParam(r, "char_name")
	if len(charName) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("empty char_name in path"))

		return
	}

	var userData *db.UserData
	if cookie, err := r.Cookie("session_id"); err != nil || len(cookie.Value) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))

		return
	} else if userData, err = db.GetUserDataBySessionId(cookie.Value); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("user data not found"))

		return
	}

	if err := r.ParseMultipartForm(20971520); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	}
	defer file.Close()

	bytes, err := io.ReadAll(file)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	}

	if err = db.AddCharCard(userData.ID, charName, bytes); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	} else {
		w.Write([]byte("success"))
	}
}

func (api *API) UploadVoiceHandler(w http.ResponseWriter, r *http.Request) {
	charName := chi.URLParam(r, "char_name")
	if len(charName) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("empty char_name in path"))

		return
	}

	if cookie, err := r.Cookie("session_id"); err != nil || len(cookie.Value) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))

		return
	} else if _, err = db.GetUserDataBySessionId(cookie.Value); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("user data not found"))

		return
	}

	if err := r.ParseMultipartForm(20971520); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	}
	defer file.Close()

	bytes, err := io.ReadAll(file)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	}

	if err = db.AddVoice(charName, bytes); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	} else {
		w.Write([]byte("success"))
	}
}

func (api *API) UploadModel(w http.ResponseWriter, r *http.Request) {
	charName := chi.URLParam(r, "char_name")
	if len(charName) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("empty char_name in path"))

		return
	}

	if cookie, err := r.Cookie("session_id"); err != nil || len(cookie.Value) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))

		return
	} else if _, err = db.GetUserDataBySessionId(cookie.Value); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("user data not found"))

		return
	}

	if err := r.ParseMultipartForm(20971520); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	}
	defer file.Close()

	bytes, err := io.ReadAll(file)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	}

	if err = db.AddModel(charName, bytes); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	} else {
		w.Write([]byte("success"))
	}
}

func (api *API) GetModel(w http.ResponseWriter, r *http.Request) {
	charName := chi.URLParam(r, "char_name")
	if len(charName) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("empty char_name in path"))

		return
	}

	modelData, err := db.GetModel(charName)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	}

	w.Header().Add("Content-Length", strconv.Itoa(len(modelData)))
	w.Header().Add("Content-Type", "application/zip, application/octet-stream")
	w.Header().Add("Last-Modified", "Mon, 24 Jul 2017 17:26:46 GMT")
	w.Header().Add("Date", "Mon, 24 Jul 2017 17:29:32 GMT")
	w.Header().Add("Cache-Control", "max-age=31536000, immutable")

	_, err = w.Write(modelData)
	// _, err = w.Write([]byte(base64.StdEncoding.EncodeToString(modelData)))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	}
}
