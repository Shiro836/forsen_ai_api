package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dchest/uniuri"
)

const twitchClientID = "zi6vy3y3iq38svpmlub5fd26uwsee8"

type UserLoginData struct {
	UserId   int
	UserName string
}

const getUsersUrl = "https://api.twitch.tv/helix/users"

type getUsersDataEntry struct {
	Id    string `json:"id"`
	Login string `json:"login"`
}

type getUsersResp struct {
	Data []getUsersDataEntry `json:"data"`
}

func getUsers(accessToken string) (*UserLoginData, error) {
	req, err := http.NewRequest(http.MethodGet, getUsersUrl, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create getUsers request: %w", err)
	}

	req.Header.Add("Authorization", "Bearer "+accessToken)
	req.Header.Add("Client-Id", twitchClientID)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to do getUsers request: %w", err)
	}

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read getUsers response body: %w", err)
	}

	respJson := &getUsersResp{}

	if err = json.Unmarshal(respData, &respJson); err != nil {
		return nil, fmt.Errorf("failed to unmarshal getUsers response body: %w", err)
	}

	if len(respJson.Data) == 0 {
		return nil, fmt.Errorf("no users found")
	}

	intId, err := strconv.Atoi(respJson.Data[0].Id)
	if err != nil {
		return nil, fmt.Errorf("failed to convert userId to int; %w", err)
	}

	return &UserLoginData{
		UserId:   intId,
		UserName: respJson.Data[0].Login,
	}, nil
}

type UserData struct {
	RefreshToken  string
	AccessToken   string
	UserLoginData *UserLoginData

	Session string
}

type userTokenData struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

func codeHandler(code string) (*UserData, error) {
	data := url.Values{}
	data.Set("client_id", twitchClientID)
	data.Set("client_secret", twitchSecret)
	data.Set("code", code)
	data.Set("grant_type", "authorization_code")
	data.Set("redirect_uri", "https://forsen.fun/twitch_token_handler")

	encodedData := data.Encode()

	resp, err := http.Post("https://id.twitch.tv/oauth2/token", "application/x-www-form-urlencoded", strings.NewReader(encodedData))
	if err != nil {
		respData, _ := io.ReadAll(resp.Body)

		return nil, fmt.Errorf("failed to send post request to token url: %w| body: %s", err, respData)
	}

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read the token response body: %w", err)
	}

	if resp.StatusCode > 299 {
		return nil, fmt.Errorf("status code (%d) of post request to token url is invalid: %s", resp.StatusCode, string(respData))
	}

	respJson := &userTokenData{}
	if err = json.Unmarshal(respData, &respJson); err != nil {
		return nil, fmt.Errorf("failed to unmarshal json: %w", err)
	}

	userData, err := getUsers(respJson.AccessToken)
	if err != nil {
		fmt.Println(err)
	} else {
		fmt.Println(userData)
	}

	return &UserData{
		RefreshToken:  respJson.RefreshToken,
		AccessToken:   respJson.AccessToken,
		UserLoginData: userData,
	}, nil
}

func addSession(userData *UserData) *UserData {
	userData.Session = uniuri.New()
	return userData
}

func onUserData(userData *UserData) (string, error) {
	session := uniuri.New()

	_, err := db.Exec(`
		insert into user_data(
			login,
			user_id,
			refresh_token,
			access_token,
			session
		) values (
			$1,
			$2,
			$3,
			$4,
			$5
		) on conflict(user_id) do update set
			login = excluded.login,
			refresh_token = excluded.refresh_token,
			access_token = excluded.access_token,
			session = excluded.session
		where excluded.user_id = user_data.user_id;
	`,
		userData.UserLoginData.UserName,
		userData.UserLoginData.UserId,
		userData.RefreshToken,
		userData.AccessToken,
		session,
	)
	if err != nil {
		return "", fmt.Errorf("failed to insert user data: %w", err)
	}

	return session, nil
}

func twitchTokenHandler(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if len(code) == 0 {
		fmt.Println(r.URL.Query().Get("error"), r.URL.Query().Get("error_description"))
	} else if userData, err := codeHandler(code); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
	} else if session, err := onUserData(addSession(userData)); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
	} else {
		http.SetCookie(w, &http.Cookie{Name: "session_id", Value: session})

		http.Redirect(w, r, "https://forsen.fun/settings", http.StatusSeeOther)
	}
}
