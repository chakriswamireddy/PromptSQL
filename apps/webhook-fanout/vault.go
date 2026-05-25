package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// VaultClient fetches webhook secrets from HashiCorp Vault.
type VaultClient struct {
	addr   string
	token  string
	mu     sync.Mutex
	cache  map[string]cacheEntry
	client *http.Client
}

type cacheEntry struct {
	secret    []byte
	expiresAt time.Time
}

func newVaultClient(addr, token string) *VaultClient {
	return &VaultClient{
		addr:  strings.TrimRight(addr, "/"),
		token: token,
		cache: make(map[string]cacheEntry),
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// GetSecret returns the secret bytes at the given Vault path.
// Results are cached for 5 minutes.
func (v *VaultClient) GetSecret(ctx context.Context, path string) ([]byte, error) {
	v.mu.Lock()
	if e, ok := v.cache[path]; ok && time.Now().Before(e.expiresAt) {
		v.mu.Unlock()
		return e.secret, nil
	}
	v.mu.Unlock()

	// Fetch from Vault KV v2.
	url := fmt.Sprintf("%s/v1/%s", v.addr, strings.TrimPrefix(path, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("vault request: %w", err)
	}
	req.Header.Set("X-Vault-Token", v.token)

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vault fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vault returned %d for %s", resp.StatusCode, path)
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var vaultResp struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &vaultResp); err != nil {
		return nil, fmt.Errorf("vault parse: %w", err)
	}

	secretStr, ok := vaultResp.Data.Data["secret"]
	if !ok {
		return nil, fmt.Errorf("vault: no 'secret' key at %s", path)
	}
	secret := []byte(secretStr)

	v.mu.Lock()
	v.cache[path] = cacheEntry{secret: secret, expiresAt: time.Now().Add(5 * time.Minute)}
	v.mu.Unlock()

	return secret, nil
}
