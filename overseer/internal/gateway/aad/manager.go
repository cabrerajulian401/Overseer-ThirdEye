package aad

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// refreshMargin treats tokens this close to expiry as expired, so a Graph
// call started "just in time" doesn't die mid-flight.
const refreshMargin = 5 * time.Minute

// backgroundTimeout bounds the network call GraphToken may make — its callers
// (scheduler ticks) carry no request context to inherit.
const backgroundTimeout = 15 * time.Second

type exchanger interface {
	Exchange(ctx context.Context, assertion string) (TokenSet, error)
	Refresh(ctx context.Context, refreshToken string) (TokenSet, error)
}

// Manager is aad mode's replacement for tokencache.Cache: one Graph
// access+refresh pair per user, keyed by the token's oid claim, filled by the
// OBO exchange on the HTTP path and renewed via the refresh grant for
// background jobs. In-memory like the cache it replaces — a restart needs one
// authed request to re-prime (documented gap, unchanged from MVP).
type Manager struct {
	obo exchanger
	log *slog.Logger

	// One mutex guards everything, held across the network calls too: MVP is
	// single-user, so serializing exchanges is simpler than singleflight and
	// also stops a stampede of ticks from double-refreshing.
	mu        sync.Mutex
	byOID     map[string]*entry
	oidByUser map[uuid.UUID]string
}

type entry struct {
	tokens TokenSet
	// needsReauth stops background refresh retries once AAD says only the
	// user can fix it; the user's next authed request clears it.
	needsReauth bool
}

func NewManager(obo exchanger, log *slog.Logger) *Manager {
	return &Manager{obo: obo, log: log, byOID: map[string]*entry{}, oidByUser: map[uuid.UUID]string{}}
}

// EnsureGraphToken returns a valid Graph token for the request's user,
// running the OBO exchange only when the cache can't serve one (first sight,
// near expiry, or a flagged reauth). The error may be entity.ErrReauthRequired.
func (m *Manager) EnsureGraphToken(ctx context.Context, userID uuid.UUID, oid, assertion string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.oidByUser[userID] = oid
	if e := m.byOID[oid]; e != nil && !e.needsReauth && time.Until(e.tokens.ExpiresAt) > refreshMargin {
		return e.tokens.AccessToken, nil
	}
	set, err := m.obo.Exchange(ctx, assertion)
	if err != nil {
		return "", fmt.Errorf("obo exchange: %w", err)
	}
	m.byOID[oid] = &entry{tokens: set}
	return set.AccessToken, nil
}

// GraphToken satisfies the usecase TokenSource ports (messages,
// subscriptions, checkups). Background callers hold no assertion to
// re-exchange, so near-expiry renews via the refresh token instead.
func (m *Manager) GraphToken(userID uuid.UUID) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	oid, ok := m.oidByUser[userID]
	if !ok {
		return "", false
	}
	e := m.byOID[oid]
	if e == nil || e.needsReauth {
		return "", false
	}
	if time.Until(e.tokens.ExpiresAt) > refreshMargin {
		return e.tokens.AccessToken, true
	}
	if e.tokens.RefreshToken == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), backgroundTimeout)
	defer cancel()
	set, err := m.obo.Refresh(ctx, e.tokens.RefreshToken)
	if err != nil {
		if errors.Is(err, entity.ErrReauthRequired) {
			e.needsReauth = true
		}
		if m.log != nil {
			m.log.Error("graph token refresh failed", "error", err)
		}
		return "", false
	}
	if set.RefreshToken == "" {
		set.RefreshToken = e.tokens.RefreshToken
	}
	e.tokens = set
	return set.AccessToken, true
}
