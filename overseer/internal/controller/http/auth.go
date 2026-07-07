package http

import (
	"context"
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

// AuthContext is what handlers read instead of the request body: userId and
// accountId always come from auth, never from the client.
type AuthContext struct {
	AccountID  string
	UserID     string
	User       entity.User
	GraphToken string
}

const authContextKey = "overseer.auth"

// AuthMiddleware requires `Authorization: Bearer <graph token>` and stashes
// the resolved AuthContext on the request. The token is the add-in's
// Graph-scoped token; Overseer calls Graph with it on the user's behalf.
func AuthMiddleware(resolver UserResolver, tokens TokenSink) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			header := c.Request().Header.Get("Authorization")
			token, ok := strings.CutPrefix(header, "Bearer ")
			if !ok || strings.TrimSpace(token) == "" {
				return c.JSON(http.StatusUnauthorized, errorResponse{Error: "missing bearer token"})
			}

			user, err := resolver.Resolve(c.Request().Context(), token)
			if err != nil {
				return c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid token"})
			}

			if tokens != nil {
				tokens.Put(user.ID, token)
			}

			c.Set(authContextKey, AuthContext{
				AccountID:  user.AccountID.String(),
				UserID:     user.ID.String(),
				User:       user,
				GraphToken: token,
			})
			return next(c)
		}
	}
}

func authFrom(c echo.Context) AuthContext {
	auth, _ := c.Get(authContextKey).(AuthContext)
	return auth
}
