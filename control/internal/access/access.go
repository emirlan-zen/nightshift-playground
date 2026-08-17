// Package access verifies Cloudflare Access identity at the application edge.
package access

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Config struct {
	TeamDomain   string
	Audience     string
	AllowedEmail string
	TrustHeader  bool
	DevMode      bool
	DevEmail     string
}

type Verifier struct {
	config  Config
	client  *http.Client
	allowed map[string]bool

	mu      sync.RWMutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

func New(cfg Config) *Verifier {
	return &Verifier{
		config:  cfg,
		client:  &http.Client{Timeout: 15 * time.Second},
		allowed: parseEmails(cfg.AllowedEmail),
		keys:    map[string]*rsa.PublicKey{},
	}
}

func (v *Verifier) Mode() string {
	switch {
	case v.config.DevMode:
		return "dev"
	case v.config.TrustHeader:
		return "insecure_header"
	default:
		return "access_jwt"
	}
}

func (v *Verifier) Warm(ctx context.Context) error {
	if v.config.TeamDomain == "" || v.config.DevMode || v.config.TrustHeader {
		return nil
	}
	return v.refresh(ctx)
}

func (v *Verifier) Handler(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email, err := v.VerifyRequest(r)
		if err != nil {
			http.Error(w, "forbidden: "+err.Error(), http.StatusForbidden)
			return
		}
		w.Header().Set("X-Authed-Email", email)
		next(w, r)
	}
}

func (v *Verifier) VerifyRequest(r *http.Request) (string, error) {
	if v.config.DevMode {
		if email := r.Header.Get("Cf-Access-Authenticated-User-Email"); email != "" {
			return email, nil
		}
		return v.config.DevEmail, nil
	}
	if v.config.TrustHeader {
		email := r.Header.Get("Cf-Access-Authenticated-User-Email")
		if email == "" {
			return "", errors.New("no auth header (insecure mode)")
		}
		if !v.emailAllowed(email) {
			return "", errors.New("email not allowed: " + email)
		}
		return email, nil
	}
	if v.config.TeamDomain == "" {
		return "", errors.New("access not configured (ACCESS_TEAM_DOMAIN)")
	}
	token := r.Header.Get("Cf-Access-Jwt-Assertion")
	if token == "" {
		if cookie, err := r.Cookie("CF_Authorization"); err == nil {
			token = cookie.Value
		}
	}
	if token == "" {
		return "", errors.New("missing Access JWT")
	}
	return v.verifyJWT(r.Context(), token)
}

func (v *Verifier) refresh(ctx context.Context) error {
	if v.config.TeamDomain == "" {
		return errors.New("ACCESS_TEAM_DOMAIN unset")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://"+v.config.TeamDomain+"/cdn-cgi/access/certs", nil)
	if err != nil {
		return err
	}
	response, err := v.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("JWKS returned HTTP %d", response.StatusCode)
	}
	var document struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		return err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, key := range document.Keys {
		if key.Kty != "RSA" {
			continue
		}
		n, err := base64.RawURLEncoding.DecodeString(key.N)
		if err != nil {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(key.E)
		if err != nil {
			continue
		}
		e := 0
		for _, b := range eBytes {
			e = e<<8 | int(b)
		}
		keys[key.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: e}
	}
	v.mu.Lock()
	v.keys, v.fetched = keys, time.Now()
	v.mu.Unlock()
	return nil
}

func (v *Verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	key, ok := v.keys[kid]
	stale := time.Since(v.fetched) > time.Hour
	v.mu.RUnlock()
	if ok && !stale {
		return key, nil
	}
	if err := v.refresh(ctx); err != nil && !ok {
		return nil, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if key, ok := v.keys[kid]; ok {
		return key, nil
	}
	return nil, errors.New("unknown kid")
}

func (v *Verifier) verifyJWT(ctx context.Context, token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("malformed jwt")
	}
	var header struct{ Alg, Kid string }
	if err := unmarshalSegment(parts[0], &header); err != nil {
		return "", err
	}
	if header.Alg != "RS256" {
		return "", errors.New("unexpected alg " + header.Alg)
	}
	publicKey, err := v.key(ctx, header.Kid)
	if err != nil {
		return "", err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, sum[:], signature); err != nil {
		return "", errors.New("bad signature")
	}
	var claims struct {
		Audience json.RawMessage `json:"aud"`
		Email    string          `json:"email"`
		Expires  int64           `json:"exp"`
		Issuer   string          `json:"iss"`
	}
	if err := unmarshalSegment(parts[1], &claims); err != nil {
		return "", err
	}
	if time.Now().Unix() >= claims.Expires {
		return "", errors.New("token expired")
	}
	if claims.Issuer != "https://"+v.config.TeamDomain {
		return "", errors.New("bad issuer")
	}
	if v.config.Audience != "" && !audContains(claims.Audience, v.config.Audience) {
		return "", errors.New("bad audience")
	}
	if claims.Email == "" {
		return "", errors.New("no email claim")
	}
	if !v.emailAllowed(claims.Email) {
		return "", errors.New("email not allowed: " + claims.Email)
	}
	return claims.Email, nil
}

func (v *Verifier) emailAllowed(email string) bool {
	return len(v.allowed) == 0 || v.allowed[strings.ToLower(email)]
}

func parseEmails(value string) map[string]bool {
	result := map[string]bool{}
	for _, email := range strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n'
	}) {
		result[strings.ToLower(strings.TrimSpace(email))] = true
	}
	return result
}

func unmarshalSegment(segment string, value any) error {
	body, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, value)
}

func audContains(raw json.RawMessage, wanted string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == wanted
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, audience := range many {
			if audience == wanted {
				return true
			}
		}
	}
	return false
}
