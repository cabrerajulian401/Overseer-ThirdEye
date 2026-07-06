package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thirdeye/overseer/internal/entity"
)

const getUserByEmailSQL = `
	SELECT u.id, u.account_id, u.email, COALESCE(u.graph_refresh_token_enc, ''), u.created_at
	FROM users u
	WHERE u.email = $1`

const insertAccountSQL = `
	INSERT INTO accounts (company_name) VALUES ($1) RETURNING id`

const insertUserSQL = `
	INSERT INTO users (account_id, email) VALUES ($1, $2) RETURNING id, created_at`

type Users struct {
	db *pgxpool.Pool
}

func NewUsers(db *pgxpool.Pool) *Users { return &Users{db: db} }

func (r *Users) GetByEmail(ctx context.Context, email string) (entity.User, error) {
	var u entity.User
	err := r.db.QueryRow(ctx, getUserByEmailSQL, email).
		Scan(&u.ID, &u.AccountID, &u.Email, &u.GraphRefreshTokenEnc, &u.CreatedAt)
	if err != nil {
		return entity.User{}, err
	}
	return u, nil
}

// EnsureSeedUser implements MVP single-user mode: seed one account + one user
// row at startup if they don't exist. Multi-user later is a login page, not a
// migration — the account_id/user_id columns already exist everywhere.
func (r *Users) EnsureSeedUser(ctx context.Context, companyName, email string) (entity.User, error) {
	u, err := r.GetByEmail(ctx, email)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return entity.User{}, fmt.Errorf("look up seed user: %w", err)
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return entity.User{}, fmt.Errorf("begin seed tx: %w", err)
	}
	defer tx.Rollback(ctx)

	u = entity.User{Email: email}
	if err := tx.QueryRow(ctx, insertAccountSQL, companyName).Scan(&u.AccountID); err != nil {
		return entity.User{}, fmt.Errorf("seed account: %w", err)
	}
	if err := tx.QueryRow(ctx, insertUserSQL, u.AccountID, email).Scan(&u.ID, &u.CreatedAt); err != nil {
		return entity.User{}, fmt.Errorf("seed user: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return entity.User{}, fmt.Errorf("commit seed tx: %w", err)
	}
	return u, nil
}
