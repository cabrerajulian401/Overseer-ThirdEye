package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/thirdeye/overseer/internal/entity"
)

type fakeAuthenticator struct {
	user  entity.User
	token string
	err   error
}

func (f fakeAuthenticator) Authenticate(_ context.Context, _ string) (entity.User, string, error) {
	if f.err != nil {
		return entity.User{}, "", f.err
	}
	return f.user, f.token, nil
}

// probeServer mounts the middleware in front of a handler that echoes what
// landed in the AuthContext, so tests see exactly what downstream sees.
func probeServer(t *testing.T, a Authenticator) *httptest.Server {
	t.Helper()
	e := echo.New()
	e.GET("/probe", func(c echo.Context) error {
		auth := authFrom(c)
		return c.JSON(http.StatusOK, map[string]string{"userId": auth.UserID, "graphToken": auth.GraphToken})
	}, AuthMiddlewareWith(a))
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, srvURL, bearer string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srvURL+"/probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, string(body)
}

func TestMiddlewareStashesGraphToken(t *testing.T) {
	user := entity.User{ID: uuid.New(), AccountID: uuid.New()}
	srv := probeServer(t, fakeAuthenticator{user: user, token: "obo-graph-token"})

	res, body := get(t, srv.URL, "incoming-app-token")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	// The handler must see the exchanged Graph token, never the incoming one.
	if got["graphToken"] != "obo-graph-token" || got["userId"] != user.ID.String() {
		t.Errorf("got %+v", got)
	}
}

func TestMiddlewareMissingHeaderIs401EmptyBody(t *testing.T) {
	srv := probeServer(t, fakeAuthenticator{user: entity.User{ID: uuid.New()}, token: "x"})

	res, body := get(t, srv.URL, "")
	if res.StatusCode != http.StatusUnauthorized || body != "" {
		t.Fatalf("status = %d body = %q, want 401 with empty body", res.StatusCode, body)
	}
}

func TestMiddlewareAuthFailureIs401EmptyBody(t *testing.T) {
	srv := probeServer(t, fakeAuthenticator{err: errors.New("bad signature")})

	res, body := get(t, srv.URL, "garbage")
	if res.StatusCode != http.StatusUnauthorized || body != "" {
		t.Fatalf("status = %d body = %q, want 401 with empty body", res.StatusCode, body)
	}
}

func TestMiddlewareReauthRequiredIs403WithCode(t *testing.T) {
	srv := probeServer(t, fakeAuthenticator{err: fmt.Errorf("obo exchange: %w", entity.ErrReauthRequired)})

	res, body := get(t, srv.URL, "valid-but-consent-revoked")
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.StatusCode)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	if got["error"] != "interaction_required" {
		t.Errorf(`body = %q, want {"error":"interaction_required"}`, body)
	}
}
