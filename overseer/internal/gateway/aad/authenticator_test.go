package aad

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

type fakeValidator struct {
	claims Claims
	err    error
	gotRaw string
}

func (f *fakeValidator) Validate(_ context.Context, raw string) (Claims, error) {
	f.gotRaw = raw
	return f.claims, f.err
}

type fakeEnsurer struct {
	token        string
	err          error
	calls        int
	gotUser      uuid.UUID
	gotOID       string
	gotAssertion string
}

func (f *fakeEnsurer) EnsureGraphToken(_ context.Context, userID uuid.UUID, oid, assertion string) (string, error) {
	f.calls++
	f.gotUser, f.gotOID, f.gotAssertion = userID, oid, assertion
	return f.token, f.err
}

func TestAuthenticateHappyPath(t *testing.T) {
	user := entity.User{ID: uuid.New(), AccountID: uuid.New(), Email: "op@distributor.com"}
	val := &fakeValidator{claims: Claims{OID: "oid-1"}}
	ens := &fakeEnsurer{token: "graph-tok"}
	a := Authenticator{Validator: val, Tokens: ens, User: user}

	gotUser, graphToken, err := a.Authenticate(context.Background(), "incoming-jwt")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if gotUser.ID != user.ID || graphToken != "graph-tok" {
		t.Errorf("user = %v, token = %q", gotUser.ID, graphToken)
	}
	if val.gotRaw != "incoming-jwt" || ens.gotOID != "oid-1" || ens.gotAssertion != "incoming-jwt" || ens.gotUser != user.ID {
		t.Errorf("plumbing: raw=%q oid=%q assertion=%q user=%v", val.gotRaw, ens.gotOID, ens.gotAssertion, ens.gotUser)
	}
}

func TestAuthenticateInvalidTokenSkipsExchange(t *testing.T) {
	val := &fakeValidator{err: errors.New("bad signature")}
	ens := &fakeEnsurer{token: "graph-tok"}
	a := Authenticator{Validator: val, Tokens: ens, User: entity.User{ID: uuid.New()}}

	if _, _, err := a.Authenticate(context.Background(), "garbage"); err == nil {
		t.Fatal("want error")
	}
	if ens.calls != 0 {
		t.Errorf("EnsureGraphToken called %d times for an invalid token", ens.calls)
	}
}

func TestAuthenticatePropagatesReauthRequired(t *testing.T) {
	val := &fakeValidator{claims: Claims{OID: "oid-1"}}
	ens := &fakeEnsurer{err: &AADError{Code: "invalid_grant", Suberror: "consent_required"}}
	a := Authenticator{Validator: val, Tokens: ens, User: entity.User{ID: uuid.New()}}

	_, _, err := a.Authenticate(context.Background(), "incoming-jwt")
	if !errors.Is(err, entity.ErrReauthRequired) {
		t.Fatalf("err = %v, want Is(ErrReauthRequired)", err)
	}
}
