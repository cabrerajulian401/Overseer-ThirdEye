package http

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/thirdeye/overseer/internal/entity"
)

// UserResolver turns a bearer token into the authenticated user. MVP
// single-user mode resolves every valid-looking token to the seeded user;
// multi-user later swaps this implementation, not the middleware.
type UserResolver interface {
	Resolve(ctx context.Context, token string) (entity.User, error)
}

// TokenSink receives the Graph token from every authed request so background
// jobs (webhook handling, subscription renewal) can call Graph later.
type TokenSink interface {
	Put(userID uuid.UUID, token string)
}

// Authenticator turns a bearer token into the authenticated user plus the
// Graph token downstream code should use. mvp mode passes the bearer straight
// through; aad mode validates it as our app's token and OBO-exchanges it.
type Authenticator interface {
	Authenticate(ctx context.Context, bearer string) (user entity.User, graphToken string, err error)
}

// AuthContext is what handlers read instead of the request body: userId and
// accountId always come from auth, never from the client.
type AuthContext struct {
	AccountID  string
	UserID     string
	User       entity.User
	GraphToken string
}

const authContextKey = "overseer.auth"

// mvpAuthenticator is the current shortcut expressed as an Authenticator: any
// resolvable bearer IS the Graph token, and the sink keeps it warm for
// background jobs.
type mvpAuthenticator struct {
	resolver UserResolver
	tokens   TokenSink
}

func (a mvpAuthenticator) Authenticate(ctx context.Context, bearer string) (entity.User, string, error) {
	user, err := a.resolver.Resolve(ctx, bearer)
	if err != nil {
		return entity.User{}, "", err
	}
	if a.tokens != nil {
		a.tokens.Put(user.ID, bearer)
	}
	return user, bearer, nil
}

// AuthMiddleware is the mvp-mode constructor, kept so existing wiring and
// tests build unchanged.
func AuthMiddleware(resolver UserResolver, tokens TokenSink) echo.MiddlewareFunc {
	return AuthMiddlewareWith(mvpAuthenticator{resolver: resolver, tokens: tokens})
}

// AuthMiddlewareWith requires `Authorization: Bearer <token>` and stashes the
// resolved AuthContext on the request. Failures are 401 with an empty body
// (the documented contract — nothing about why leaks to callers), except the
// one case the user must fix themselves: a Graph consent/interaction error,
// surfaced as 403 {"error":"interaction_required"} for the FE to map to
// "reconnect your mailbox".
func AuthMiddlewareWith(a Authenticator) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			header := c.Request().Header.Get("Authorization")
			token, ok := strings.CutPrefix(header, "Bearer ")
			if !ok || strings.TrimSpace(token) == "" {
				return c.NoContent(http.StatusUnauthorized)
			}

			user, graphToken, err := a.Authenticate(c.Request().Context(), token)
			if err != nil {
				if errors.Is(err, entity.ErrReauthRequired) {
					return c.JSON(http.StatusForbidden, errorResponse{Error: "interaction_required"})
				}
				return c.NoContent(http.StatusUnauthorized)
			}

			c.Set(authContextKey, AuthContext{
				AccountID:  user.AccountID.String(),
				UserID:     user.ID.String(),
				User:       user,
				GraphToken: graphToken,
			})
			return next(c)
		}
	}
}

func authFrom(c echo.Context) AuthContext {
	auth, _ := c.Get(authContextKey).(AuthContext)
	return auth
}
