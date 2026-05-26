package unleash

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: 3 * time.Second},
	}
}

func (c *Client) IsEnabled(flag string) bool {
	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("%s/api/client/features/%s", c.baseURL, flag), nil)
	if err != nil {
		return false
	}
	if c.token != "" {
		req.Header.Set("Authorization", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// Default to enabled when Unleash is unreachable (fail-open for existing services).
		return true
	}
	defer resp.Body.Close()

	var f struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&f); err != nil {
		return true
	}
	return f.Enabled
}
