package gokick

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
)

const (
	defaultAPIBaseURL = "https://api.kick.com"
	authBaseURL       = "https://id.kick.com"
)

type Client struct {
	options   *ClientOptions
	mu        sync.Mutex
	callbacks clientCallbacks
}

type onUserAccessTokenRefreshedCallback func(accessToken, refreshToken string)

type clientCallbacks struct {
	onUserAccessTokenRefreshed onUserAccessTokenRefreshedCallback
}

type ClientOptions struct {
	AppAccessToken   string
	UserAccessToken  string
	UserRefreshToken string
	HTTPClient       *http.Client
	APIBaseURL       string
	AuthBaseURL      string
	ClientID         string
	ClientSecret     string
}

func NewClient(options *ClientOptions) (*Client, error) {
	if options.APIBaseURL == "" {
		options.APIBaseURL = defaultAPIBaseURL
	}

	if options.AuthBaseURL == "" {
		options.AuthBaseURL = authBaseURL
	}

	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{}
	}

	return &Client{
		options: options,
		mu:      sync.Mutex{},
	}, nil
}

type errorResponse struct {
	Data    interface{} `json:"data"`
	Message string      `json:"message"`
}

type authErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	Message          string `json:"message"`
}

func (c *Client) SetAppAccessToken(token string) {
	c.mu.Lock()
	c.options.AppAccessToken = token
	c.mu.Unlock()
}

func (c *Client) SetUserAccessToken(token string) {
	c.mu.Lock()
	c.options.UserAccessToken = token
	c.mu.Unlock()
}

func (c *Client) SetUserRefreshToken(token string) {
	c.mu.Lock()
	c.options.UserRefreshToken = token
	c.mu.Unlock()
}

func (c *Client) OnUserAccessTokenRefreshed(callback onUserAccessTokenRefreshedCallback) {
	c.mu.Lock()
	c.callbacks.onUserAccessTokenRefreshed = callback
	c.mu.Unlock()
}

func (c *Client) buildURL(base, path string) string {
	return fmt.Sprintf("%s%s", base, path)
}

func (c *Client) setRequestHeaders(req *http.Request) {
	var token string
	if c.options.AppAccessToken != "" {
		token = c.options.AppAccessToken
	}
	if c.options.UserAccessToken != "" {
		token = c.options.UserAccessToken
	}

	if token != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	}
}

func (c *Client) refreshToken(ctx context.Context) error {
	token, err := c.RefreshToken(ctx, c.options.UserRefreshToken)
	if err != nil {
		return fmt.Errorf("failed to refresh token: %w", err)
	}

	c.mu.Lock()
	c.options.UserAccessToken = token.AccessToken
	c.options.UserRefreshToken = token.RefreshToken
	c.mu.Unlock()

	if callback := c.callbacks.onUserAccessTokenRefreshed; callback != nil {
		go callback(token.AccessToken, token.RefreshToken)
	}

	return nil
}

type contextKey string

const retryKey contextKey = "retry"

func (c *Client) do(req *http.Request) (*http.Response, error) {
	var bodyReader *bytes.Reader
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read request body: %w", err)
		}
		req.Body.Close()

		bodyReader = bytes.NewReader(bodyBytes)
		req.Body = io.NopCloser(bodyReader)
	}

	for {
		c.setRequestHeaders(req)

		response, err := c.options.HTTPClient.Do(req)
		if err != nil {
			return nil, err
		}

		if response.StatusCode == http.StatusUnauthorized && c.canRefreshUserToken() {
			ctx := req.Context()
			if ctx.Value(retryKey) == nil {
				ctx = context.WithValue(ctx, retryKey, true)
			} else {
				return response, nil
			}

			err := c.refreshToken(ctx)
			if err != nil {
				return nil, err
			}

			if bodyReader != nil {
				_, err := bodyReader.Seek(0, io.SeekStart)
				if err != nil {
					return nil, fmt.Errorf("failed to reset request body: %w", err)
				}
			}

			continue
		}

		return response, nil
	}
}

func (c *Client) canRefreshUserToken() bool {
	return c.options.ClientID != "" &&
		c.options.ClientSecret != "" &&
		c.options.UserRefreshToken != ""
}
