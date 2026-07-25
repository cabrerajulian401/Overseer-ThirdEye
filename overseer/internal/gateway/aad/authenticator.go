package aad

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

type claimsValidator interface {
	Validate(ctx context.Context, raw string) (Claims, error)
}

type graphEnsurer interface {
	EnsureGraphToken(ctx context.Context, userID uuid.UUID, oid, assertion string) (string, error)
}

// Authenticator is aad mode's answer to the http layer's Authenticator seam:
// validate our app's token, OBO-exchange it for the Graph token, resolve the
// user. Still single-user — User is the seeded operator; multi-user later
// resolves from Claims here, nowhere else.
type Authenticator struct {
	Validator claimsValidator
	Tokens    graphEnsurer
	User      entity.User
}

func (a Authenticator) Authenticate(ctx context.Context, bearer string) (entity.User, string, error) {
	claims, err := a.Validator.Validate(ctx, bearer)
	if err != nil {
		return entity.User{}, "", fmt.Errorf("validate token: %w", err)
	}
	graphToken, err := a.Tokens.EnsureGraphToken(ctx, a.User.ID, claims.OID, bearer)
	if err != nil {
		return entity.User{}, "", err
	}
	return a.User, graphToken, nil
}
