package auth

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const (
	backupCodeCount  = 10
	backupCodeLen    = 10 // characters in base32 (50 bits)
	totpIssuerPrefix = "GovernancePlatform"
)

// MFAService manages TOTP enrollment and verification for users.
type MFAService struct {
	pool   *pgxpool.Pool
	issuer string
}

// NewMFAService returns an MFAService.
func NewMFAService(pool *pgxpool.Pool, issuer string) *MFAService {
	if issuer == "" {
		issuer = totpIssuerPrefix
	}
	return &MFAService{pool: pool, issuer: issuer}
}

// EnrollResult is returned when MFA enrollment starts.
type EnrollResult struct {
	Secret      string   `json:"secret"`        // base32 TOTP secret (show to user once)
	OTPURL      string   `json:"otpUrl"`        // otpauth:// URI for QR code
	BackupCodes []string `json:"backupCodes"`   // plaintext backup codes (show once)
}

// StartEnroll generates a new TOTP secret, stores it unverified, and returns the
// enrollment payload. The user must call ConfirmEnroll with a valid TOTP code to
// activate MFA. Any previous unverified enrollment is replaced.
func (m *MFAService) StartEnroll(ctx context.Context, userID, tenantID, email string) (*EnrollResult, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      m.issuer,
		AccountName: email,
		SecretSize:  20,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return nil, fmt.Errorf("mfa: generate totp key: %w", err)
	}

	// Remove any existing unverified TOTP (idempotent re-enroll).
	_, _ = m.pool.Exec(ctx,
		"DELETE FROM mfa_credentials WHERE user_id = $1 AND kind = 'totp' AND is_verified = false",
		userID)

	if _, err := m.pool.Exec(ctx,
		`INSERT INTO mfa_credentials (user_id, tenant_id, kind, secret_enc, is_verified)
		 VALUES ($1, $2, 'totp', $3, false)`,
		userID, tenantID, key.Secret(),
	); err != nil {
		return nil, fmt.Errorf("mfa: store totp secret: %w", err)
	}

	backupCodes, err := m.generateBackupCodes(ctx, userID, tenantID)
	if err != nil {
		return nil, err
	}
	return &EnrollResult{
		Secret:      key.Secret(),
		OTPURL:      key.URL(),
		BackupCodes: backupCodes,
	}, nil
}

// ConfirmEnroll verifies the first TOTP code and marks the credential as active.
func (m *MFAService) ConfirmEnroll(ctx context.Context, userID, totpCode string) error {
	secret, credID, err := m.fetchTOTPSecret(ctx, userID, false)
	if err != nil {
		return err
	}
	if !totp.Validate(totpCode, secret) {
		return ErrInvalidTOTP
	}
	_, err = m.pool.Exec(ctx,
		"UPDATE mfa_credentials SET is_verified = true WHERE id = $1", credID)
	return err
}

// Verify checks a TOTP code against the user's verified secret.
// Returns ErrMFANotEnrolled if the user has no verified TOTP.
func (m *MFAService) Verify(ctx context.Context, userID, totpCode string) error {
	secret, _, err := m.fetchTOTPSecret(ctx, userID, true)
	if err != nil {
		return err
	}
	if !totp.Validate(totpCode, secret) {
		return ErrInvalidTOTP
	}
	return nil
}

// IsEnrolled returns true if the user has a verified TOTP credential.
func (m *MFAService) IsEnrolled(ctx context.Context, userID string) (bool, error) {
	var count int
	err := m.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM mfa_credentials WHERE user_id = $1 AND kind = 'totp' AND is_verified = true",
		userID,
	).Scan(&count)
	return count > 0, err
}

// Disable removes all MFA credentials for the user.
// The caller must independently verify the user's password and a valid TOTP code
// before calling Disable.
func (m *MFAService) Disable(ctx context.Context, userID string) error {
	_, err := m.pool.Exec(ctx,
		"DELETE FROM mfa_credentials WHERE user_id = $1", userID)
	return err
}

// fetchTOTPSecret returns the TOTP secret and credential ID for userID.
// If verified=true, only returns verified credentials.
func (m *MFAService) fetchTOTPSecret(ctx context.Context, userID string, verified bool) (secret, credID string, err error) {
	q := "SELECT id, secret_enc FROM mfa_credentials WHERE user_id = $1 AND kind = 'totp'"
	if verified {
		q += " AND is_verified = true"
	}
	err = m.pool.QueryRow(ctx, q, userID).Scan(&credID, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrMFANotEnrolled
	}
	return secret, credID, err
}

// generateBackupCodes creates backupCodeCount single-use codes, hashes them,
// and stores the hashes in mfa_credentials (kind='backup').
func (m *MFAService) generateBackupCodes(ctx context.Context, userID, tenantID string) ([]string, error) {
	// Delete existing backup codes.
	_, _ = m.pool.Exec(ctx,
		"DELETE FROM mfa_credentials WHERE user_id = $1 AND kind = 'backup'", userID)

	var codes []string
	for range backupCodeCount {
		raw := make([]byte, 8) // 8 bytes → 13 base32 chars
		if _, err := rand.Read(raw); err != nil {
			return nil, fmt.Errorf("mfa: generate backup code: %w", err)
		}
		code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)[:backupCodeLen]
		codes = append(codes, code)

		hash, err := HashPassword(code)
		if err != nil {
			return nil, fmt.Errorf("mfa: hash backup code: %w", err)
		}
		if _, err := m.pool.Exec(ctx,
			`INSERT INTO mfa_credentials (user_id, tenant_id, kind, secret_enc, is_verified)
			 VALUES ($1, $2, 'backup', $3, true)`,
			userID, tenantID, hash,
		); err != nil {
			return nil, fmt.Errorf("mfa: store backup code: %w", err)
		}
	}
	return codes, nil
}

// UseBackupCode consumes a single-use backup code if valid.
// Returns ErrInvalidTOTP on mismatch; ErrMFANotEnrolled if no backup codes remain.
func (m *MFAService) UseBackupCode(ctx context.Context, userID, code string) error {
	rows, err := m.pool.Query(ctx,
		`SELECT id, secret_enc FROM mfa_credentials
		 WHERE user_id = $1 AND kind = 'backup' AND used_at IS NULL`,
		userID)
	if err != nil {
		return fmt.Errorf("mfa: query backup codes: %w", err)
	}
	defer rows.Close()

	type entry struct{ id, hash string }
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.hash); err != nil {
			return err
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return ErrMFANotEnrolled
	}

	for _, e := range entries {
		ok, err := VerifyPassword(code, e.hash)
		if err != nil || !ok {
			continue
		}
		_, err = m.pool.Exec(ctx,
			"UPDATE mfa_credentials SET used_at = $1 WHERE id = $2", time.Now(), e.id)
		return err
	}
	return ErrInvalidTOTP
}

var (
	ErrMFANotEnrolled = fmt.Errorf("mfa: not enrolled")
	ErrInvalidTOTP    = fmt.Errorf("mfa: invalid code")
)
