package twitch

import (
	"app/db"
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

type userTokenData struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

func (c *Client) CodeHandler(code string) (*db.User, error) {
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

	client, err := c.NewHelixClient(respJson.AccessToken, respJson.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create helix client: %w", err)
	}

	usersResp, err := client.GetUsers(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get users: %w", err)
	}

	if usersResp == nil || len(usersResp.Data.Users) == 0 {
		return nil, fmt.Errorf("no users found")
	}

	userID, err := strconv.Atoi(usersResp.Data.Users[0].ID)
	if err != nil {
		return nil, fmt.Errorf("failed to convert userId to int: %w", err)
	}

	return &db.User{
		TwitchUserID:       userID,
		TwitchLogin:        usersResp.Data.Users[0].Login,
		TwitchRefreshToken: respJson.RefreshToken,
		TwitchAccessToken:  respJson.AccessToken,
	}, nil
}

func (c *Client) NewHelixAppClient() (*helix.Client, error) {
	return helix.NewClient(&helix.Options{
		HTTPClient: c.httpClient,

		ClientID:     c.cfg.ClientID,
		ClientSecret: c.cfg.Secret,
	})
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
