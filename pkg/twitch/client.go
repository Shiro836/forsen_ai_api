package twitch

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/nicklaw5/helix/v2"
)

type Config struct {
	Secret   string `yaml:"secret"`
	ClientID string `yaml:"client_id"`
}

var _ HTTPClient = http.DefaultClient

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type Client struct {
	httpClient HTTPClient
	cfg        *Config
}

func New(httpClient HTTPClient, cfg *Config) *Client {
	return &Client{
		httpClient: httpClient,
		cfg:        cfg,
	}
}

const getUsersUrl = "https://api.twitch.tv/helix/users"

type getUsersDataEntry struct {
	Id    string `json:"id"`
	Login string `json:"login"`
}

type getUsersResp struct {
	Data []getUsersDataEntry `json:"data"`
}

type TwitchUserData struct {
	UserID   int
	Username string
}

func (c *Client) GetUsers(accessToken string) (*TwitchUserData, error) {
	req, err := http.NewRequest(http.MethodGet, getUsersUrl, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create getUsers request: %w", err)
	}

	req.Header.Add("Authorization", "Bearer "+accessToken)
	req.Header.Add("Client-Id", c.cfg.ClientID)

	resp, err := c.httpClient.Do(req)
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

	return &TwitchUserData{
		UserID:   intId,
		Username: respJson.Data[0].Login,
	}, nil
}

type userTokenData struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

type TwitchUserCredsWithUserData struct {
	UserID       int
	Username     string
	AccessToken  string
	RefreshToken string
}

func (c *Client) CodeHandler(code string) (*TwitchUserCredsWithUserData, error) {
	data := url.Values{}
	data.Set("client_id", c.cfg.ClientID)
	data.Set("client_secret", c.cfg.Secret)
	data.Set("code", code)
	data.Set("grant_type", "authorization_code")
	data.Set("redirect_uri", "https://forsen.fun/twitch_token_handler")

	encodedData := data.Encode()

	resp, err := http.Post("https://id.twitch.tv/oauth2/token", "application/x-www-form-urlencoded", strings.NewReader(encodedData))
	if err != nil {
		respData, _ := io.ReadAll(resp.Body)

		return nil, fmt.Errorf("failed to send post request to token url: %w| body: %s", err, string(respData))
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

	userData, err := c.GetUsers(respJson.AccessToken)
	if err != nil {
		fmt.Println(err)
	} else {
		fmt.Println(userData)
	}

	return &TwitchUserCredsWithUserData{
		UserID:       userData.UserID,
		Username:     userData.Username,
		RefreshToken: respJson.RefreshToken,
		AccessToken:  respJson.AccessToken,
	}, nil
}

func (c *Client) NewHelixClient(accessToken, refreshToken string) (*helix.Client, error) {
	return helix.NewClient(&helix.Options{
		HTTPClient: c.httpClient,

		ClientID:        c.cfg.ClientID,
		ClientSecret:    c.cfg.Secret,
		UserAccessToken: accessToken,
		RefreshToken:    refreshToken,
	})
}
