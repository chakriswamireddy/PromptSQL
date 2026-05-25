package main

import (
	"context"
	"testing"
)

func TestSSRFPrivateIPBlocked(t *testing.T) {
	cases := []struct {
		url     string
		wantErr bool
	}{
		{"https://169.254.169.254/latest/meta-data/", true},  // AWS link-local
		{"https://10.0.0.1/secret", true},                    // RFC1918
		{"https://192.168.1.1/admin", true},                   // RFC1918
		{"https://127.0.0.1/", true},                          // loopback
		// Note: real external hosts would resolve in tests — skip them.
	}
	for _, tc := range cases {
		_, err := validateWebhookURL(context.Background(), tc.url)
		if tc.wantErr && err == nil {
			t.Errorf("expected SSRF block for %s", tc.url)
		}
	}
}

func TestSSRFHTTPSRequired(t *testing.T) {
	_, err := validateWebhookURL(context.Background(), "http://example.com/hook")
	if err == nil {
		t.Error("expected error for non-HTTPS URL")
	}
}

func TestSSRFInvalidURL(t *testing.T) {
	_, err := validateWebhookURL(context.Background(), "not-a-url")
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}
