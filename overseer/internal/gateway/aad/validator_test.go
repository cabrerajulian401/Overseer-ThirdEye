package aad

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testClientID = "client-123"
	testTenantID = "tenant-1"
	testKid      = "test-key"
)

// jwksServer serves the given kid→public-key set as a JWKS document and
// counts fetches so tests can assert the cache actually caches.
type jwksServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	keys map[string]*rsa.PublicKey
	hits int
}

func newJWKSServer(t *testing.T, keys map[string]*rsa.PublicKey) *jwksServer {
	t.Helper()
	s := &jwksServer{keys: keys}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.hits++
		var out []map[string]string
		for kid, pub := range s.keys {
			out = append(out, map[string]string{
				"kty": "RSA", "kid": kid,
				"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": out})
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *jwksServer) setKeys(keys map[string]*rsa.PublicKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = keys
}

func (s *jwksServer) fetches() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func genKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

// mint signs a token that is valid for the test validator unless mutate
// changes a claim.
func mint(t *testing.T, priv *rsa.PrivateKey, kid string, mutate func(jwt.MapClaims)) string {
	t.Helper()
	claims := jwt.MapClaims{
		"aud":                testClientID,
		"iss":                "https://login.microsoftonline.com/" + testTenantID + "/v2.0",
		"tid":                testTenantID,
		"oid":                "oid-user-1",
		"scp":                "access_as_user profile openid",
		"preferred_username": "op@distributor.com",
		"iat":                time.Now().Add(-time.Minute).Unix(),
		"exp":                time.Now().Add(time.Hour).Unix(),
	}
	if mutate != nil {
		mutate(claims)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newValidator(jwksURL string) *Validator {
	return &Validator{ClientID: testClientID, TenantID: testTenantID, JWKSURL: jwksURL}
}

func TestValidateGoodToken(t *testing.T) {
	priv := genKey(t)
	jwks := newJWKSServer(t, map[string]*rsa.PublicKey{testKid: &priv.PublicKey})
	v := newValidator(jwks.srv.URL)

	claims, err := v.Validate(context.Background(), mint(t, priv, testKid, nil))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if claims.OID != "oid-user-1" || claims.TID != testTenantID || claims.Email != "op@distributor.com" {
		t.Errorf("claims = %+v", claims)
	}
}

func TestValidateAcceptsAPIURIAudience(t *testing.T) {
	priv := genKey(t)
	jwks := newJWKSServer(t, map[string]*rsa.PublicKey{testKid: &priv.PublicKey})
	v := newValidator(jwks.srv.URL)

	tok := mint(t, priv, testKid, func(c jwt.MapClaims) { c["aud"] = "api://addin.example.com/" + testClientID })
	if _, err := v.Validate(context.Background(), tok); err != nil {
		t.Fatalf("api:// audience should validate: %v", err)
	}
}

func TestValidateRejectsBadSignature(t *testing.T) {
	priv, imposter := genKey(t), genKey(t)
	jwks := newJWKSServer(t, map[string]*rsa.PublicKey{testKid: &priv.PublicKey})
	v := newValidator(jwks.srv.URL)

	// Same kid, different private key: signature must not verify.
	if _, err := v.Validate(context.Background(), mint(t, imposter, testKid, nil)); err == nil {
		t.Fatal("token signed by the wrong key validated")
	}
}

func TestValidateRejectsWrongAudience(t *testing.T) {
	priv := genKey(t)
	jwks := newJWKSServer(t, map[string]*rsa.PublicKey{testKid: &priv.PublicKey})
	v := newValidator(jwks.srv.URL)

	tok := mint(t, priv, testKid, func(c jwt.MapClaims) { c["aud"] = "someone-else" })
	if _, err := v.Validate(context.Background(), tok); err == nil {
		t.Fatal("token for another audience validated")
	}
}

func TestValidateRejectsExpired(t *testing.T) {
	priv := genKey(t)
	jwks := newJWKSServer(t, map[string]*rsa.PublicKey{testKid: &priv.PublicKey})
	v := newValidator(jwks.srv.URL)

	tok := mint(t, priv, testKid, func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() })
	if _, err := v.Validate(context.Background(), tok); err == nil {
		t.Fatal("expired token validated")
	}
}

func TestValidateRejectsMissingScope(t *testing.T) {
	priv := genKey(t)
	jwks := newJWKSServer(t, map[string]*rsa.PublicKey{testKid: &priv.PublicKey})
	v := newValidator(jwks.srv.URL)

	tok := mint(t, priv, testKid, func(c jwt.MapClaims) { c["scp"] = "profile openid" })
	if _, err := v.Validate(context.Background(), tok); err == nil {
		t.Fatal("token without access_as_user validated")
	}
}

func TestValidateRejectsWrongTenant(t *testing.T) {
	priv := genKey(t)
	jwks := newJWKSServer(t, map[string]*rsa.PublicKey{testKid: &priv.PublicKey})
	v := newValidator(jwks.srv.URL)

	tok := mint(t, priv, testKid, func(c jwt.MapClaims) {
		c["tid"] = "other-tenant"
		c["iss"] = "https://login.microsoftonline.com/other-tenant/v2.0"
	})
	if _, err := v.Validate(context.Background(), tok); err == nil {
		t.Fatal("token from another tenant validated")
	}
}

func TestValidateRejectsV1Issuer(t *testing.T) {
	priv := genKey(t)
	jwks := newJWKSServer(t, map[string]*rsa.PublicKey{testKid: &priv.PublicKey})
	v := newValidator(jwks.srv.URL)

	tok := mint(t, priv, testKid, func(c jwt.MapClaims) { c["iss"] = "https://sts.windows.net/" + testTenantID + "/" })
	if _, err := v.Validate(context.Background(), tok); err == nil {
		t.Fatal("v1 issuer validated")
	}
}

func TestValidateCachesKeysAndRefetchesOnRotation(t *testing.T) {
	priv, priv2 := genKey(t), genKey(t)
	jwks := newJWKSServer(t, map[string]*rsa.PublicKey{testKid: &priv.PublicKey})
	v := newValidator(jwks.srv.URL)

	// Two validations, one fetch: the cache works.
	if _, err := v.Validate(context.Background(), mint(t, priv, testKid, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Validate(context.Background(), mint(t, priv, testKid, nil)); err != nil {
		t.Fatal(err)
	}
	if jwks.fetches() != 1 {
		t.Fatalf("fetches = %d, want 1", jwks.fetches())
	}

	// Key rotation: a new kid appears. Within the refetch rate limit the
	// unknown kid is an error; after the window it refetches and validates.
	jwks.setKeys(map[string]*rsa.PublicKey{testKid: &priv.PublicKey, "rotated": &priv2.PublicKey})
	if _, err := v.Validate(context.Background(), mint(t, priv2, "rotated", nil)); err == nil {
		t.Fatal("unknown kid inside the rate-limit window should fail, not refetch")
	}
	v.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := v.Validate(context.Background(), mint(t, priv2, "rotated", nil)); err != nil {
		t.Fatalf("rotated key after rate-limit window: %v", err)
	}
	if jwks.fetches() != 2 {
		t.Fatalf("fetches = %d, want 2", jwks.fetches())
	}
}

func TestValidateGarbageToken(t *testing.T) {
	priv := genKey(t)
	jwks := newJWKSServer(t, map[string]*rsa.PublicKey{testKid: &priv.PublicKey})
	v := newValidator(jwks.srv.URL)

	if _, err := v.Validate(context.Background(), "not-a-jwt"); err == nil {
		t.Fatal("garbage validated")
	}
}
