package aad

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

type fakeOBO struct {
	exchangeSet   TokenSet
	exchangeErr   error
	refreshSet    TokenSet
	refreshErr    error
	exchangeCalls int
	refreshCalls  int
	lastAssertion string
	lastRefresh   string
}

func (f *fakeOBO) Exchange(_ context.Context, assertion string) (TokenSet, error) {
	f.exchangeCalls++
	f.lastAssertion = assertion
	return f.exchangeSet, f.exchangeErr
}

func (f *fakeOBO) Refresh(_ context.Context, refreshToken string) (TokenSet, error) {
	f.refreshCalls++
	f.lastRefresh = refreshToken
	return f.refreshSet, f.refreshErr
}

func freshSet(access string) TokenSet {
	return TokenSet{AccessToken: access, RefreshToken: "refresh-" + access, ExpiresAt: time.Now().Add(time.Hour)}
}

func TestEnsureExchangesOnFirstSight(t *testing.T) {
	obo := &fakeOBO{exchangeSet: freshSet("g1")}
	m := NewManager(obo, nil)
	userID := uuid.New()

	got, err := m.EnsureGraphToken(context.Background(), userID, "oid-1", "assertion-1")
	if err != nil || got != "g1" {
		t.Fatalf("got %q, %v", got, err)
	}
	if obo.exchangeCalls != 1 || obo.lastAssertion != "assertion-1" {
		t.Errorf("exchangeCalls = %d, assertion = %q", obo.exchangeCalls, obo.lastAssertion)
	}
}

func TestEnsureServesCacheWhileValid(t *testing.T) {
	obo := &fakeOBO{exchangeSet: freshSet("g1")}
	m := NewManager(obo, nil)
	userID := uuid.New()

	if _, err := m.EnsureGraphToken(context.Background(), userID, "oid-1", "a1"); err != nil {
		t.Fatal(err)
	}
	got, err := m.EnsureGraphToken(context.Background(), userID, "oid-1", "a2")
	if err != nil || got != "g1" {
		t.Fatalf("got %q, %v", got, err)
	}
	if obo.exchangeCalls != 1 {
		t.Errorf("exchangeCalls = %d, want 1 (cache must serve the second request)", obo.exchangeCalls)
	}
}

func TestEnsureReExchangesNearExpiry(t *testing.T) {
	obo := &fakeOBO{exchangeSet: TokenSet{AccessToken: "g1", RefreshToken: "r1", ExpiresAt: time.Now().Add(time.Minute)}}
	m := NewManager(obo, nil)
	userID := uuid.New()

	if _, err := m.EnsureGraphToken(context.Background(), userID, "oid-1", "a1"); err != nil {
		t.Fatal(err)
	}
	obo.exchangeSet = freshSet("g2")
	got, err := m.EnsureGraphToken(context.Background(), userID, "oid-1", "a2")
	if err != nil || got != "g2" {
		t.Fatalf("got %q, %v", got, err)
	}
	if obo.exchangeCalls != 2 {
		t.Errorf("exchangeCalls = %d, want 2 (inside the refresh margin)", obo.exchangeCalls)
	}
}

func TestGraphTokenServesCache(t *testing.T) {
	obo := &fakeOBO{exchangeSet: freshSet("g1")}
	m := NewManager(obo, nil)
	userID := uuid.New()

	if _, err := m.EnsureGraphToken(context.Background(), userID, "oid-1", "a1"); err != nil {
		t.Fatal(err)
	}
	if got, ok := m.GraphToken(userID); !ok || got != "g1" {
		t.Fatalf("got %q, %v", got, ok)
	}
	if obo.refreshCalls != 0 {
		t.Errorf("refreshCalls = %d, want 0", obo.refreshCalls)
	}
}

func TestGraphTokenRefreshesNearExpiry(t *testing.T) {
	obo := &fakeOBO{
		exchangeSet: TokenSet{AccessToken: "g1", RefreshToken: "r1", ExpiresAt: time.Now().Add(time.Minute)},
		refreshSet:  freshSet("g2"),
	}
	m := NewManager(obo, nil)
	userID := uuid.New()

	if _, err := m.EnsureGraphToken(context.Background(), userID, "oid-1", "a1"); err != nil {
		t.Fatal(err)
	}
	if got, ok := m.GraphToken(userID); !ok || got != "g2" {
		t.Fatalf("got %q, %v — want the refreshed token", got, ok)
	}
	if obo.refreshCalls != 1 || obo.lastRefresh != "r1" {
		t.Errorf("refreshCalls = %d, lastRefresh = %q", obo.refreshCalls, obo.lastRefresh)
	}
	// The refreshed set is now cached: no second network call.
	if got, ok := m.GraphToken(userID); !ok || got != "g2" {
		t.Fatalf("got %q, %v", got, ok)
	}
	if obo.refreshCalls != 1 {
		t.Errorf("refreshCalls = %d, want still 1", obo.refreshCalls)
	}
}

func TestGraphTokenKeepsOldRefreshTokenWhenOmitted(t *testing.T) {
	obo := &fakeOBO{
		exchangeSet: TokenSet{AccessToken: "g1", RefreshToken: "r1", ExpiresAt: time.Now().Add(time.Minute)},
		// AAD may omit refresh_token on a refresh grant; the old one stays valid.
		refreshSet: TokenSet{AccessToken: "g2", ExpiresAt: time.Now().Add(time.Minute)},
	}
	m := NewManager(obo, nil)
	userID := uuid.New()

	if _, err := m.EnsureGraphToken(context.Background(), userID, "oid-1", "a1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.GraphToken(userID); !ok {
		t.Fatal("first refresh failed")
	}
	if _, ok := m.GraphToken(userID); !ok {
		t.Fatal("second refresh failed")
	}
	if obo.lastRefresh != "r1" {
		t.Errorf("lastRefresh = %q, want the original r1 carried forward", obo.lastRefresh)
	}
}

func TestGraphTokenReauthRequiredStopsRetrying(t *testing.T) {
	obo := &fakeOBO{
		exchangeSet: TokenSet{AccessToken: "g1", RefreshToken: "r1", ExpiresAt: time.Now().Add(time.Minute)},
		refreshErr:  &AADError{Code: "invalid_grant", Suberror: "consent_required"},
	}
	m := NewManager(obo, nil)
	userID := uuid.New()

	if _, err := m.EnsureGraphToken(context.Background(), userID, "oid-1", "a1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.GraphToken(userID); ok {
		t.Fatal("consent-revoked user should have no token")
	}
	if _, ok := m.GraphToken(userID); ok {
		t.Fatal("still no token")
	}
	if obo.refreshCalls != 1 {
		t.Errorf("refreshCalls = %d, want 1 — a flagged user must not be retried every tick", obo.refreshCalls)
	}

	// The user's next request re-exchanges and recovers the flow.
	obo.exchangeSet = freshSet("g3")
	if got, err := m.EnsureGraphToken(context.Background(), userID, "oid-1", "a2"); err != nil || got != "g3" {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, ok := m.GraphToken(userID); !ok || got != "g3" {
		t.Fatalf("got %q, %v — recovery should clear the reauth flag", got, ok)
	}
}

func TestEnsurePropagatesReauthRequired(t *testing.T) {
	obo := &fakeOBO{exchangeErr: &AADError{Code: "invalid_grant", Suberror: "consent_required"}}
	m := NewManager(obo, nil)

	_, err := m.EnsureGraphToken(context.Background(), uuid.New(), "oid-1", "a1")
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, entity.ErrReauthRequired) {
		t.Fatalf("err = %v, want Is(ErrReauthRequired)", err)
	}
}

func TestGraphTokenUnknownUser(t *testing.T) {
	m := NewManager(&fakeOBO{}, nil)
	if _, ok := m.GraphToken(uuid.New()); ok {
		t.Fatal("unknown user must have no token")
	}
}
