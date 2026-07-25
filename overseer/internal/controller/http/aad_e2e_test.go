package http

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/thirdeye/overseer/internal/controller/ws"
	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/gateway/aad"
)

const (
	aadTestClientID = "client-123"
	aadTestTenantID = "tenant-1"
	aadTestKid      = "test-key"
)

// aadEnv is the whole aad-mode stack on fakes: a JWKS server holding the test
// signing key, a scripted token endpoint, and the real router + hub behind
// RouterDeps.Auth — the same shape main.go wires in aad mode.
type aadEnv struct {
	srv       *httptest.Server
	hub       *ws.Hub
	priv      *rsa.PrivateKey
	tokenHits atomic.Int64
}

func newAADEnv(t *testing.T, tokenHandler http.HandlerFunc) *aadEnv {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	env := &aadEnv{priv: priv}

	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pub := &priv.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": aadTestKid,
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	}))
	t.Cleanup(jwks.Close)

	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.tokenHits.Add(1)
		tokenHandler(w, r)
	}))
	t.Cleanup(tokens.Close)

	user := entity.User{ID: uuid.New(), AccountID: uuid.New(), Email: "op@distributor.com"}
	manager := aad.NewManager(&aad.OBOClient{
		TenantID: aadTestTenantID, ClientID: aadTestClientID, ClientSecret: "s3cret", TokenURL: tokens.URL,
	}, nil)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	env.hub = ws.NewHub(log)

	e := NewRouter(RouterDeps{
		Auth: aad.Authenticator{
			Validator: &aad.Validator{ClientID: aadTestClientID, TenantID: aadTestTenantID, JWKSURL: jwks.URL},
			Tokens:    manager,
			User:      user,
		},
		WS:  env.hub,
		Log: log,
	})
	env.srv = httptest.NewServer(e)
	t.Cleanup(env.srv.Close)
	return env
}

func aadGrantOK(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "graph-access-1", "refresh_token": "graph-refresh-1", "expires_in": 3600,
	})
}

func aadGrantConsentRequired(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": "invalid_grant", "suberror": "consent_required",
		"error_description": "AADSTS65001: the user or administrator has not consented",
	})
}

func (e *aadEnv) mint(t *testing.T, mutate func(jwt.MapClaims)) string {
	t.Helper()
	claims := jwt.MapClaims{
		"aud":                aadTestClientID,
		"iss":                "https://login.microsoftonline.com/" + aadTestTenantID + "/v2.0",
		"tid":                aadTestTenantID,
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
	tok.Header["kid"] = aadTestKid
	s, err := tok.SignedString(e.priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// dialWS attempts the /ws upgrade and returns the handshake status, response
// body (empty on success), and the connection (nil unless upgraded).
func (e *aadEnv) dialWS(t *testing.T, rawQuery string) (int, string, *websocket.Conn) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws"
	if rawQuery != "" {
		wsURL += "?" + rawQuery
	}
	conn, res, _ := websocket.DefaultDialer.Dial(wsURL, nil)
	if res == nil {
		t.Fatalf("dial %s: no handshake response", wsURL)
	}
	if conn != nil {
		t.Cleanup(func() { conn.Close() })
	}
	var body []byte
	if res.Body != nil {
		body, _ = io.ReadAll(res.Body)
		res.Body.Close()
	}
	return res.StatusCode, string(body), conn
}

func TestAADWSQueryTokenUpgrades(t *testing.T) {
	env := newAADEnv(t, aadGrantOK)

	status, _, conn := env.dialWS(t, "token="+env.mint(t, nil))
	if status != http.StatusSwitchingProtocols || conn == nil {
		t.Fatalf("status = %d, want 101 with live connection", status)
	}

	// Registered on the hub, not just upgraded.
	env.hub.Publish(entity.Event{Type: entity.EventSyncCompleted, Data: entity.SyncCompleted{}})
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, msg, err := conn.ReadMessage(); err != nil {
		t.Fatalf("expected hub broadcast: %v", err)
	} else if !strings.Contains(string(msg), string(entity.EventSyncCompleted)) {
		t.Fatalf("broadcast payload = %s", msg)
	}
}

func TestAADWSExpiredTokenIs401EmptyBody(t *testing.T) {
	env := newAADEnv(t, aadGrantOK)

	expired := env.mint(t, func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() })
	status, body, _ := env.dialWS(t, "token="+expired)
	if status != http.StatusUnauthorized || body != "" {
		t.Fatalf("status = %d body = %q, want 401 empty", status, body)
	}
	if env.tokenHits.Load() != 0 {
		t.Errorf("token endpoint hit %d times for an invalid token", env.tokenHits.Load())
	}
}

func TestAADWSNoCredentialsIs401(t *testing.T) {
	env := newAADEnv(t, aadGrantOK)

	status, body, _ := env.dialWS(t, "")
	if status != http.StatusUnauthorized || body != "" {
		t.Fatalf("status = %d body = %q, want 401 empty", status, body)
	}
}

func TestAADOBOExchangeHappensOncePerUser(t *testing.T) {
	env := newAADEnv(t, aadGrantOK)

	for i := range 2 {
		status, _, _ := env.dialWS(t, "token="+env.mint(t, nil))
		if status != http.StatusSwitchingProtocols {
			t.Fatalf("dial %d: status = %d", i, status)
		}
	}
	if env.tokenHits.Load() != 1 {
		t.Fatalf("token endpoint hits = %d, want 1 (cache must serve the second request)", env.tokenHits.Load())
	}
}

func TestAADConsentRequiredIs403WithCode(t *testing.T) {
	env := newAADEnv(t, aadGrantConsentRequired)

	status, body, _ := env.dialWS(t, "token="+env.mint(t, nil))
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
	if !strings.Contains(body, "interaction_required") {
		t.Fatalf("body = %q, want interaction_required code", body)
	}
}
