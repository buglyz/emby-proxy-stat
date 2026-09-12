package auth

import (
	"strings"
	"testing"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	const password = "correct horse battery staple"
	hash, err := GeneratePasswordHash(password)
	if err != nil {
		t.Fatalf("generate hash: %v", err)
	}
	if !strings.HasPrefix(hash, HashAlgorithm+"$") {
		t.Fatalf("unexpected hash format: %q", hash)
	}
	if !VerifyPassword(password, hash, "") {
		t.Fatal("expected password to verify")
	}
	if VerifyPassword("wrong password", hash, "") {
		t.Fatal("unexpected password verification")
	}
}

func TestVerifyPasswordPlaintextFallback(t *testing.T) {
	if !VerifyPassword("secret", "", "secret") {
		t.Fatal("expected plaintext fallback to verify")
	}
	if VerifyPassword("other", "", "secret") {
		t.Fatal("plaintext fallback must reject wrong password")
	}
}

func TestParsePasswordHashRejectsBadInput(t *testing.T) {
	bad := []string{
		"",
		"plaintext",
		"pbkdf2-sha256$abc$salt$digest",
		"pbkdf2-sha256$1000$" + "AAAAAAAAAAAAAAAAAAAAAA$" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"pbkdf2-sha256$210000$short$" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	}
	for _, value := range bad {
		if _, _, _, err := ParsePasswordHash(value); err == nil {
			t.Errorf("expected %q to be rejected", value)
		}
	}
}
