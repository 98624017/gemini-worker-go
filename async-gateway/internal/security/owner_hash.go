package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

func NormalizeBearerToken(header string) (string, error) {
	trimmed := strings.TrimSpace(header)
	if trimmed == "" {
		return "", fmt.Errorf("authorization header is required")
	}

	parts := strings.Fields(trimmed)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", fmt.Errorf("authorization header must use Bearer token")
	}
	if parts[1] == "" {
		return "", fmt.Errorf("authorization token is required")
	}

	return parts[1], nil
}

func DeriveOwnerHash(secret, authorizationHeader string) (string, error) {
	token, err := NormalizeBearerToken(authorizationHeader)
	if err != nil {
		return "", err
	}

	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write([]byte(token)); err != nil {
		return "", fmt.Errorf("write owner hash payload: %w", err)
	}

	return hex.EncodeToString(mac.Sum(nil)), nil
}
