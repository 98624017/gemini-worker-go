package security

import "testing"

func TestNormalizeBearerTokenRejectsMissingOrMalformedAuthorization(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		header string
	}{
		{name: "missing header", header: ""},
		{name: "missing scheme", header: "sk-test"},
		{name: "wrong scheme", header: "Basic sk-test"},
		{name: "missing token", header: "Bearer    "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := NormalizeBearerToken(tc.header); err == nil {
				t.Fatalf("expected error for %q", tc.header)
			}
		})
	}
}

func TestDeriveOwnerHashStableForSameAPIKey(t *testing.T) {
	t.Parallel()

	const secret = "owner-secret"
	const authHeader = "Bearer sk-test-key"

	first, err := DeriveOwnerHash(secret, authHeader)
	if err != nil {
		t.Fatalf("DeriveOwnerHash() first error = %v", err)
	}

	second, err := DeriveOwnerHash(secret, "bearer   sk-test-key")
	if err != nil {
		t.Fatalf("DeriveOwnerHash() second error = %v", err)
	}

	if first != second {
		t.Fatalf("owner hash mismatch: %q != %q", first, second)
	}
}

func TestDeriveOwnerHashChangesWhenAPIKeyChanges(t *testing.T) {
	t.Parallel()

	const secret = "owner-secret"

	first, err := DeriveOwnerHash(secret, "Bearer sk-test-key")
	if err != nil {
		t.Fatalf("DeriveOwnerHash() first error = %v", err)
	}

	second, err := DeriveOwnerHash(secret, "Bearer sk-other-key")
	if err != nil {
		t.Fatalf("DeriveOwnerHash() second error = %v", err)
	}

	if first == second {
		t.Fatalf("expected different owner hashes")
	}
}
