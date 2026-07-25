// Package aad is the Azure AD adapter for AUTH_MODE=aad: it validates the
// add-in's Office-SSO tokens against Microsoft's published signing keys and
// trades them for Graph tokens via the on-behalf-of flow. Like every gateway
// it holds no business decisions — the http middleware and main.go decide
// when to call it.
package aad

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// DefaultJWKSURL is the common-tenant discovery document; every AAD v2 token
// verifies against it regardless of tenant — tenant binding is the iss/tid
// check, not the key set.
const DefaultJWKSURL = "https://login.microsoftonline.com/common/discovery/v2.0/keys"

// RequiredScope is the delegated scope Scout requests via Office SSO; a token
// without it was minted for some other purpose and must not reach our API.
const RequiredScope = "access_as_user"

// jwksMinRefetch keeps a flood of made-up kids from turning every bad request
// into a JWKS fetch.
const jwksMinRefetch = time.Minute

// Claims is the slice of the validated token the rest of the system needs.
type Claims struct {
	OID   string // stable per-user object id — the token-cache key
	TID   string
	Email string // preferred_username, informational only
}

// Validator checks Office-SSO access tokens for our app registration:
// signature via the cached JWKS, audience, v2 issuer, expiry, and scope.
type Validator struct {
	ClientID   string
	TenantID   string
	JWKSURL    string       // defaults to DefaultJWKSURL
	HTTPClient *http.Client // defaults to http.DefaultClient

	now func() time.Time // test seam for the refetch rate limit

	mu          sync.Mutex
	keys        map[string]*rsa.PublicKey
	lastFetched time.Time
}

func (v *Validator) Validate(ctx context.Context, raw string) (Claims, error) {
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30*time.Second),
	)
	claims := jwt.MapClaims{}
	if _, err := parser.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, fmt.Errorf("token has no kid header")
		}
		return v.keyFor(ctx, kid)
	}); err != nil {
		return Claims{}, fmt.Errorf("parse token: %w", err)
	}

	aud, err := claims.GetAudience()
	if err != nil || !v.audienceOK(aud) {
		return Claims{}, fmt.Errorf("token audience %v is not this app", aud)
	}
	tid, _ := claims["tid"].(string)
	if tid != v.TenantID {
		return Claims{}, fmt.Errorf("token tenant %q is not ours", tid)
	}
	if iss, _ := claims.GetIssuer(); iss != "https://login.microsoftonline.com/"+tid+"/v2.0" {
		return Claims{}, fmt.Errorf("issuer %q is not the v2 endpoint for tenant %q", iss, tid)
	}
	scp, _ := claims["scp"].(string)
	if !slices.Contains(strings.Fields(scp), RequiredScope) {
		return Claims{}, fmt.Errorf("token lacks the %s scope", RequiredScope)
	}
	oid, _ := claims["oid"].(string)
	if oid == "" {
		return Claims{}, fmt.Errorf("token has no oid claim")
	}
	email, _ := claims["preferred_username"].(string)
	return Claims{OID: oid, TID: tid, Email: email}, nil
}

// audienceOK accepts the bare client id and the api://<domain>/<client-id>
// Application ID URI — Office SSO mints either depending on manifest setup.
func (v *Validator) audienceOK(aud jwt.ClaimStrings) bool {
	for _, a := range aud {
		if a == v.ClientID {
			return true
		}
		if strings.HasPrefix(a, "api://") && strings.HasSuffix(a, "/"+v.ClientID) {
			return true
		}
	}
	return false
}

func (v *Validator) keyFor(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	now := time.Now
	if v.now != nil {
		now = v.now
	}
	if !v.lastFetched.IsZero() && now().Sub(v.lastFetched) < jwksMinRefetch {
		return nil, fmt.Errorf("unknown signing key %q", kid)
	}
	if err := v.fetchKeysLocked(ctx); err != nil {
		return nil, err
	}
	v.lastFetched = now()
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown signing key %q", kid)
}

func (v *Validator) fetchKeysLocked(ctx context.Context) error {
	jwksURL := v.JWKSURL
	if jwksURL == "" {
		jwksURL = DefaultJWKSURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return fmt.Errorf("build jwks request: %w", err)
	}
	client := v.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks endpoint returned %d", res.StatusCode)
	}
	var doc struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&doc); err != nil {
		return fmt.Errorf("decode jwks: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		if errN != nil || errE != nil || len(n) == 0 || len(e) == 0 {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	v.keys = keys
	return nil
}
