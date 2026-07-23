package http

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/thirdeye/overseer/internal/controller/ws"
	"github.com/thirdeye/overseer/internal/entity"
)

// pickyResolver accepts exactly one token — unlike testutil.StaticResolver,
// which accepts anything, so it can't distinguish valid from invalid tokens.
type pickyResolver struct {
	token string
	user  entity.User
}

func (r pickyResolver) Resolve(_ context.Context, token string) (entity.User, error) {
	if token != r.token {
		return entity.User{}, errors.New("invalid token")
	}
	return r.user, nil
}

// syncBuffer lets the test read the log while server goroutines write it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type wsEnv struct {
	srv *httptest.Server
	hub *ws.Hub
	log *syncBuffer
}

// newWSEnv builds the real router with a real ws.Hub behind a resolver that
// only accepts validToken, logging into an inspectable buffer.
func newWSEnv(t *testing.T, validToken string) *wsEnv {
	t.Helper()

	user := entity.User{ID: uuid.New(), AccountID: uuid.New(), Email: "op@distributor.com"}
	logBuf := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logBuf, nil))
	hub := ws.NewHub(log)

	e := NewRouter(RouterDeps{
		Resolver: pickyResolver{token: validToken, user: user},
		WS:       hub,
		Log:      log,
	})

	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return &wsEnv{srv: srv, hub: hub, log: logBuf}
}

// dial attempts the /ws upgrade. rawQuery goes on the URL verbatim; header is
// an optional Authorization value. Returns the HTTP status of the handshake
// response and the connection (nil unless the upgrade succeeded).
func (e *wsEnv) dial(t *testing.T, rawQuery, authHeader string) (int, *websocket.Conn) {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws"
	if rawQuery != "" {
		wsURL += "?" + rawQuery
	}
	var hdr http.Header
	if authHeader != "" {
		hdr = http.Header{"Authorization": []string{authHeader}}
	}

	conn, res, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if res == nil {
		t.Fatalf("dial %s: no handshake response (err: %v)", wsURL, err)
	}
	if conn != nil {
		t.Cleanup(func() { conn.Close() })
	}
	return res.StatusCode, conn
}

func TestWSUpgradeWithAuthorizationHeader(t *testing.T) {
	e := newWSEnv(t, "good-token")

	status, conn := e.dial(t, "", "Bearer good-token")
	if status != http.StatusSwitchingProtocols || conn == nil {
		t.Fatalf("status = %d, want %d with live connection", status, http.StatusSwitchingProtocols)
	}
}

func TestWSUpgradeWithQueryTokenOnly(t *testing.T) {
	e := newWSEnv(t, "good-token")

	status, conn := e.dial(t, "token=good-token", "")
	if status != http.StatusSwitchingProtocols || conn == nil {
		t.Fatalf("status = %d, want %d with live connection", status, http.StatusSwitchingProtocols)
	}

	// The connection must be registered on the hub, not just upgraded.
	e.hub.Publish(entity.Event{Type: entity.EventSyncCompleted, Data: entity.SyncCompleted{}})
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, msg, err := conn.ReadMessage(); err != nil {
		t.Fatalf("expected hub broadcast on query-token connection: %v", err)
	} else if !strings.Contains(string(msg), string(entity.EventSyncCompleted)) {
		t.Fatalf("broadcast payload = %s, want event type %q", msg, entity.EventSyncCompleted)
	}
}

func TestWSUpgradeWithInvalidQueryToken(t *testing.T) {
	e := newWSEnv(t, "good-token")

	status, _ := e.dial(t, "token=wrong-token", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestWSUpgradeWithNoCredentials(t *testing.T) {
	e := newWSEnv(t, "good-token")

	status, _ := e.dial(t, "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestWSHeaderWinsOverQueryToken(t *testing.T) {
	e := newWSEnv(t, "good-token")

	// Valid header + garbage query param: header must win, query ignored.
	status, conn := e.dial(t, "token=wrong-token", "Bearer good-token")
	if status != http.StatusSwitchingProtocols || conn == nil {
		t.Fatalf("status = %d, want %d (header should win)", status, http.StatusSwitchingProtocols)
	}
}

func TestWSQueryTokenIsURLDecoded(t *testing.T) {
	// Tokens can hold characters that must be percent-encoded on the wire;
	// the FE sends encodeURIComponent(jwt).
	raw := "sp3c!al tok/en+="
	e := newWSEnv(t, raw)

	status, conn := e.dial(t, "token="+url.QueryEscape(raw), "")
	if status != http.StatusSwitchingProtocols || conn == nil {
		t.Fatalf("status = %d, want %d after URL-decoding", status, http.StatusSwitchingProtocols)
	}
}

func TestHTTPEndpointsStillRejectQueryToken(t *testing.T) {
	// Query-param auth is a /ws-only concession; /orders must not accept it.
	e := newEnv(t)
	defer e.srv.Close()

	res, err := http.Get(e.srv.URL + "/orders?token=test-token")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /orders?token= status = %d, want %d", res.StatusCode, http.StatusUnauthorized)
	}
}

func TestWSTokenNeverLogged(t *testing.T) {
	const secret = "super-secret-jwt-value"
	e := newWSEnv(t, secret)

	status, _ := e.dial(t, "token="+secret, "")
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want %d", status, http.StatusSwitchingProtocols)
	}

	// The request logger fires after the handler returns; poll briefly for
	// the /ws line, then make sure the token isn't in it.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(e.log.String(), "/ws") {
		if time.Now().After(deadline) {
			t.Fatal("request log line for /ws never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if logged := e.log.String(); strings.Contains(logged, secret) {
		t.Fatalf("token leaked into request log:\n%s", logged)
	}
}
