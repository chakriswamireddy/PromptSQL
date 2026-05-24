package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2idParams are tuned for interactive logins (OWASP recommended minimums).
// Time=2, Memory=64 MB, Threads=1 gives ~250 ms on a single core.
const (
	argonTime    = 2
	argonMemory  = 64 * 1024
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword hashes password using Argon2id and returns a PHC-formatted string.
// The hash is safe to store in users.password_hash.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: generate salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword checks password against the stored PHC hash using constant-time comparison.
func VerifyPassword(password, encoded string) (bool, error) {
	params, salt, hash, err := decodePHC(encoded)
	if err != nil {
		return false, fmt.Errorf("password: decode hash: %w", err)
	}
	cmp := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, uint32(len(hash)))
	return subtle.ConstantTimeCompare(hash, cmp) == 1, nil
}

type argonParams struct {
	time    uint32
	memory  uint32
	threads uint8
}

func decodePHC(encoded string) (argonParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// $argon2id$v=19$m=...,t=...,p=...$salt$hash
	if len(parts) != 6 || parts[1] != "argon2id" {
		return argonParams{}, nil, nil, errors.New("invalid argon2id format")
	}
	var p argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return argonParams{}, nil, nil, fmt.Errorf("parse params: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return argonParams{}, nil, nil, fmt.Errorf("decode salt: %w", err)
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return argonParams{}, nil, nil, fmt.Errorf("decode hash: %w", err)
	}
	return p, salt, hash, nil
}
