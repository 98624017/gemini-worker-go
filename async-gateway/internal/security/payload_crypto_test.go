package security

import "testing"

const (
	testEncryptionKeyBase64      = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	testWrongEncryptionKeyBase64 = "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXpBQkNERUY="
)

func TestParseEncryptionKeyRejectsInvalidLength(t *testing.T) {
	t.Parallel()

	if _, err := ParseEncryptionKey("c2hvcnQ="); err == nil {
		t.Fatalf("expected key length validation error")
	}
}

func TestEncryptDecryptAuthorizationRoundTrip(t *testing.T) {
	t.Parallel()

	key, err := ParseEncryptionKey(testEncryptionKeyBase64)
	if err != nil {
		t.Fatalf("ParseEncryptionKey() error = %v", err)
	}

	ciphertext, err := EncryptAuthorization("Bearer sk-test-key", key)
	if err != nil {
		t.Fatalf("EncryptAuthorization() error = %v", err)
	}
	if len(ciphertext) == 0 {
		t.Fatalf("expected ciphertext")
	}

	plaintext, err := DecryptAuthorization(ciphertext, key)
	if err != nil {
		t.Fatalf("DecryptAuthorization() error = %v", err)
	}
	if plaintext != "Bearer sk-test-key" {
		t.Fatalf("plaintext = %q, want %q", plaintext, "Bearer sk-test-key")
	}
}

func TestDecryptAuthorizationWithWrongKeyFails(t *testing.T) {
	t.Parallel()

	key, err := ParseEncryptionKey(testEncryptionKeyBase64)
	if err != nil {
		t.Fatalf("ParseEncryptionKey() error = %v", err)
	}
	wrongKey, err := ParseEncryptionKey(testWrongEncryptionKeyBase64)
	if err != nil {
		t.Fatalf("ParseEncryptionKey() wrong key error = %v", err)
	}

	ciphertext, err := EncryptAuthorization("Bearer sk-test-key", key)
	if err != nil {
		t.Fatalf("EncryptAuthorization() error = %v", err)
	}

	if _, err := DecryptAuthorization(ciphertext, wrongKey); err == nil {
		t.Fatalf("expected decryption failure with wrong key")
	}
}
