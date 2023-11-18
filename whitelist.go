package main

import (
	"app/db"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
)

func getWhitelist(w http.ResponseWriter, r *http.Request) {
	if whitelist, err := db.GetDbWhitelist(); err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to get whitelist from db"))
	} else if sort.SliceStable(whitelist.List, func(i, j int) bool {
		if whitelist.List[j].IsMod {
			return false
		}

		if whitelist.List[i].BannedBy != nil {
			return false
		}

		return true
	}); false {

	} else if data, err := json.Marshal(whitelist); err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to marshal whitelist"))
	} else {
		w.Write(data)
	}
}

func updateWhitelist(w http.ResponseWriter, r *http.Request) {
	upd := &db.WhitelistUpdate{}

	if sessionId, err := r.Cookie("session_id"); err != nil || len(sessionId.Value) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("Unauthorized"))
	} else if userData, err := db.GetUserDataBySessionId(sessionId.Value); err != nil {
		fmt.Println(fmt.Println("failed to read settings:", err))
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("Unauthorized"))
	} else if data, err := io.ReadAll(r.Body); err != nil {
		fmt.Println(fmt.Println("failed to read request body:", err))
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	} else if err = json.Unmarshal(data, &upd); err != nil {
		fmt.Println(fmt.Println("failed to unmarshal request body:", err))
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	} else if err = db.UpdateDbWhitelist(upd, userData.UserLoginData.UserName); err != nil {
		fmt.Println(fmt.Println("failed to update db white list:", err))
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error or no access"))
	} else {
		w.Write([]byte("success"))
	}
}
