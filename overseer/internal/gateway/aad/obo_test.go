package aad

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/thirdeye/overseer/internal/entity"
)

func newTokenServer(t *testing.T, handler http.HandlerFunc) (*OBOClient, *[]map[string]string) {
	t.Helper()
	var calls []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// t.Errorf, never t.Fatalf: this runs on the server's goroutine.
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		got := map[string]string{}
		for k := range r.PostForm {
			got[k] = r.PostForm.Get(k)
		}
		calls = append(calls, got)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	c := &OBOClient{TenantID: "tenant-1", ClientID: "client-123", ClientSecret: "s3cret", TokenURL: srv.URL}
	return c, &calls
}

func grantOK(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "graph-access-1", "refresh_token": "graph-refresh-1", "expires_in": 3600,
	})
}

func TestExchangeSendsOBOGrant(t *testing.T) {
	c, calls := newTokenServer(t, grantOK)
	fixed := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return fixed }

	set, err := c.Exchange(context.Background(), "incoming-assertion")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if set.AccessToken != "graph-access-1" || set.RefreshToken != "graph-refresh-1" {
		t.Errorf("set = %+v", set)
	}
	if !set.ExpiresAt.Equal(fixed.Add(3600 * time.Second)) {
		t.Errorf("ExpiresAt = %v, want %v", set.ExpiresAt, fixed.Add(time.Hour))
	}
	form := (*calls)[0]
	if form["grant_type"] != "urn:ietf:params:oauth:grant-type:jwt-bearer" ||
		form["requested_token_use"] != "on_behalf_of" ||
		form["client_id"] != "client-123" ||
		form["client_secret"] != "s3cret" ||
		form["assertion"] != "incoming-assertion" ||
		form["scope"] != "https://graph.microsoft.com/.default offline_access" {
		t.Errorf("form = %+v", form)
	}
}

func TestRefreshSendsRefreshGrant(t *testing.T) {
	c, calls := newTokenServer(t, grantOK)

	if _, err := c.Refresh(context.Background(), "old-refresh"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	form := (*calls)[0]
	if form["grant_type"] != "refresh_token" ||
		form["refresh_token"] != "old-refresh" ||
		form["client_id"] != "client-123" ||
		form["client_secret"] != "s3cret" ||
		form["scope"] != "https://graph.microsoft.com/.default offline_access" {
		t.Errorf("form = %+v", form)
	}
}

func TestConsentErrorIsReauthRequired(t *testing.T) {
	c, _ := newTokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "invalid_grant", "suberror": "consent_required",
			"error_description": "AADSTS65001: the user or administrator has not consented",
		})
	})

	_, err := c.Exchange(context.Background(), "assertion")
	if !errors.Is(err, entity.ErrReauthRequired) {
		t.Fatalf("err = %v, want Is(ErrReauthRequired)", err)
	}
}

func TestInteractionRequiredCodeIsReauthRequired(t *testing.T) {
	c, _ := newTokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "interaction_required", "error_description": "AADSTS50076: MFA required",
		})
	})

	_, err := c.Refresh(context.Background(), "old-refresh")
	if !errors.Is(err, entity.ErrReauthRequired) {
		t.Fatalf("err = %v, want Is(ErrReauthRequired)", err)
	}
}

func TestPlainTokenErrorIsNotReauth(t *testing.T) {
	c, _ := newTokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "invalid_client", "error_description": "AADSTS7000215: invalid client secret",
		})
	})

	_, err := c.Exchange(context.Background(), "assertion")
	if err == nil {
		t.Fatal("want error")
	}
	if errors.Is(err, entity.ErrReauthRequired) {
		t.Fatalf("bad client secret is an ops problem, not a user reauth: %v", err)
	}
}

func TestNonJSONErrorBody(t *testing.T) {
	c, _ := newTokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream exploded"))
	})

	if _, err := c.Exchange(context.Background(), "assertion"); err == nil {
		t.Fatal("want error on non-JSON failure body")
	}
}
