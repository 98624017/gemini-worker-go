package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

const encryptionKeyBytes = 32

func ParseEncryptionKey(raw string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("TASK_PAYLOAD_ENCRYPTION_KEY must be base64: %w", err)
	}
	if len(decoded) != encryptionKeyBytes {
		return nil, fmt.Errorf("TASK_PAYLOAD_ENCRYPTION_KEY must decode to %d bytes", encryptionKeyBytes)
	}
	return decoded, nil
}

func EncryptAuthorization(authorization string, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("read nonce: %w", err)
	}

	encrypted := aead.Seal(nil, nonce, []byte(authorization), nil)
	return append(nonce, encrypted...), nil
}

func DecryptAuthorization(ciphertext, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}

	if len(ciphertext) < aead.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce := ciphertext[:aead.NonceSize()]
	payload := ciphertext[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, payload, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt authorization: %w", err)
	}

	return string(plaintext), nil
}
