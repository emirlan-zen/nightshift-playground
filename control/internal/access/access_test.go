package access

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func signJWT(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	segment := func(value any) string {
		body, _ := json.Marshal(value)
		return base64.RawURLEncoding.EncodeToString(body)
	}
	payload := segment(map[string]string{"alg": "RS256", "kid": kid}) + "." + segment(claims)
	sum := sha256.Sum256([]byte(payload))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return payload + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func TestVerifyJWT(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	verifier := New(Config{TeamDomain: "test.cloudflareaccess.com", Audience: "aud123"})
	verifier.keys = map[string]*rsa.PublicKey{"k1": &key.PublicKey}
	verifier.fetched = time.Now()
	base := func() map[string]any {
		return map[string]any{
			"aud": "aud123", "email": "ops@example.com",
			"exp": time.Now().Add(time.Hour).Unix(), "iss": "https://test.cloudflareaccess.com",
		}
	}

	request := httptest.NewRequest("GET", "/", nil)
	if got, err := verifier.verifyJWT(request.Context(), signJWT(t, key, "k1", base())); err != nil || got != "ops@example.com" {
		t.Fatalf("valid token = %q, %v", got, err)
	}
	expired := base()
	expired["exp"] = time.Now().Add(-time.Minute).Unix()
	if _, err := verifier.verifyJWT(request.Context(), signJWT(t, key, "k1", expired)); err == nil {
		t.Fatal("expected expired-token error")
	}
	wrongAudience := base()
	wrongAudience["aud"] = "other"
	if _, err := verifier.verifyJWT(request.Context(), signJWT(t, key, "k1", wrongAudience)); err == nil {
		t.Fatal("expected audience error")
	}
	wrongIssuer := base()
	wrongIssuer["iss"] = "https://evil.example.com"
	if _, err := verifier.verifyJWT(request.Context(), signJWT(t, key, "k1", wrongIssuer)); err == nil {
		t.Fatal("expected issuer error")
	}
}

func TestVerifyRequestModes(t *testing.T) {
	dev := New(Config{DevMode: true, DevEmail: "dev@nightshift.local"})
	if got, err := dev.VerifyRequest(httptest.NewRequest("GET", "/", nil)); err != nil || got != "dev@nightshift.local" {
		t.Fatalf("dev = %q, %v", got, err)
	}

	trusted := New(Config{TrustHeader: true, AllowedEmail: "ops@example.com"})
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Cf-Access-Authenticated-User-Email", "ops@example.com")
	if got, err := trusted.VerifyRequest(request); err != nil || got != "ops@example.com" {
		t.Fatalf("trusted = %q, %v", got, err)
	}
	request.Header.Set("Cf-Access-Authenticated-User-Email", "intruder@example.com")
	if _, err := trusted.VerifyRequest(request); err == nil {
		t.Fatal("expected allowlist error")
	}
}

func TestAudContains(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want bool
	}{{`"aud123"`, true}, {`"nope"`, false}, {`["a","aud123"]`, true}, {`["a"]`, false}, {`123`, false}} {
		if got := audContains(json.RawMessage(test.raw), "aud123"); got != test.want {
			t.Errorf("audContains(%s) = %v", test.raw, got)
		}
	}
}
