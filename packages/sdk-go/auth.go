package governance

import (
	"context"
	"net/http"
)

// AuthClient handles authentication operations.
type AuthClient struct{ c *Client }

// LoginRequest holds login credentials.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	TenantID string `json:"tenant_id"`
}

// LoginResponse is returned on successful authentication.
type LoginResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// Login authenticates and sets the client token automatically.
func (a *AuthClient) Login(ctx context.Context, req LoginRequest) (*LoginResponse, error) {
	var resp LoginResponse
	if err := a.c.do(ctx, http.MethodPost, "/v1/auth/login", req, &resp); err != nil {
		return nil, err
	}
	a.c.SetToken(resp.AccessToken)
	return &resp, nil
}

// Refresh exchanges a refresh token for a new access token.
func (a *AuthClient) Refresh(ctx context.Context, refreshToken string) (*LoginResponse, error) {
	var resp LoginResponse
	err := a.c.do(ctx, http.MethodPost, "/v1/auth/refresh",
		map[string]string{"refresh_token": refreshToken}, &resp)
	if err != nil {
		return nil, err
	}
	a.c.SetToken(resp.AccessToken)
	return &resp, nil
}

// Logout invalidates the current session.
func (a *AuthClient) Logout(ctx context.Context) error {
	return a.c.do(ctx, http.MethodPost, "/v1/auth/logout", nil, nil)
}

// InitiateStepUp begins a step-up MFA challenge.
func (a *AuthClient) InitiateStepUp(ctx context.Context, obligationToken string) error {
	return a.c.do(ctx, http.MethodPost, "/v1/auth/step-up/initiate",
		map[string]string{"obligation_token": obligationToken}, nil)
}

// CompleteStepUp completes MFA verification.
func (a *AuthClient) CompleteStepUp(ctx context.Context, obligationToken, mfaCode string) (*LoginResponse, error) {
	var resp LoginResponse
	err := a.c.do(ctx, http.MethodPost, "/v1/auth/step-up/complete",
		map[string]string{"obligation_token": obligationToken, "mfa_code": mfaCode}, &resp)
	return &resp, err
}
