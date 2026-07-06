package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thirdeye/overseer/internal/entity"
)

const upsertOrderSQL = `
	INSERT INTO orders (account_id, user_id, conversation_id, company_name,
	                    email_address, order_id, email_url, status,
	                    original_est_date, current_est_date, last_messenger, supporting_quote)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	ON CONFLICT (user_id, conversation_id) DO UPDATE SET
		company_name = EXCLUDED.company_name,
		email_address = EXCLUDED.email_address,
		order_id = EXCLUDED.order_id,
		email_url = EXCLUDED.email_url,
		status = EXCLUDED.status,
		original_est_date = EXCLUDED.original_est_date,
		current_est_date = EXCLUDED.current_est_date,
		last_messenger = EXCLUDED.last_messenger,
		supporting_quote = EXCLUDED.supporting_quote,
		updated_at = now()` // idempotent — sync re-runs and Graph re-deliveries both land here

const listOrdersSQL = `
	SELECT id, account_id, user_id, conversation_id, company_name,
	       email_address, order_id, email_url, status,
	       original_est_date, current_est_date, last_messenger, supporting_quote,
	       created_at, updated_at
	FROM orders
	WHERE user_id = $1
	  AND ($2::text IS NULL OR status = $2)
	  AND ($3::timestamptz IS NULL OR created_at < $3)
	ORDER BY created_at DESC
	LIMIT $4`

const getOrderByConversationSQL = `
	SELECT id, account_id, user_id, conversation_id, company_name,
	       email_address, order_id, email_url, status,
	       original_est_date, current_est_date, last_messenger, supporting_quote,
	       created_at, updated_at
	FROM orders
	WHERE user_id = $1 AND conversation_id = $2` // the webhook-job lookup (idx_orders_conversation)

type Orders struct {
	db *pgxpool.Pool
}

func NewOrders(db *pgxpool.Pool) *Orders { return &Orders{db: db} }

func (r *Orders) Upsert(ctx context.Context, o entity.Order) error {
	_, err := r.db.Exec(ctx, upsertOrderSQL,
		o.AccountID, o.UserID, o.ConversationID, o.CompanyName,
		nullString(o.EmailAddress), o.OrderID, nullString(o.EmailURL), string(o.Status),
		o.OriginalEstDate, o.CurrentEstDate, nullString(o.LastMessenger), nullString(o.SupportingQuote))
	if err != nil {
		return fmt.Errorf("upsert order: %w", err)
	}
	return nil
}

func (r *Orders) List(ctx context.Context, userID uuid.UUID, status *entity.OrderStatus, before *time.Time, limit int) ([]entity.Order, error) {
	var statusStr *string
	if status != nil {
		s := string(*status)
		statusStr = &s
	}
	rows, err := r.db.Query(ctx, listOrdersSQL, userID, statusStr, before, limit)
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	defer rows.Close()
	return scanOrders(rows)
}

func (r *Orders) GetByConversation(ctx context.Context, userID string, conversationID string) (entity.Order, error) {
	rows, err := r.db.Query(ctx, getOrderByConversationSQL, userID, conversationID)
	if err != nil {
		return entity.Order{}, fmt.Errorf("get order by conversation: %w", err)
	}
	defer rows.Close()
	out, err := scanOrders(rows)
	if err != nil {
		return entity.Order{}, err
	}
	if len(out) == 0 {
		return entity.Order{}, pgx.ErrNoRows
	}
	return out[0], nil
}

func scanOrders(rows pgx.Rows) ([]entity.Order, error) {
	var out []entity.Order
	for rows.Next() {
		var (
			o                                                      entity.Order
			emailAddress, emailURL, lastMessenger, supportingQuote *string
			orderID                                                *int
			status                                                 string
		)
		if err := rows.Scan(
			&o.ID, &o.AccountID, &o.UserID, &o.ConversationID, &o.CompanyName,
			&emailAddress, &orderID, &emailURL, &status,
			&o.OriginalEstDate, &o.CurrentEstDate, &lastMessenger, &supportingQuote,
			&o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan order: %w", err)
		}
		o.Status = entity.OrderStatus(status)
		o.EmailAddress = deref(emailAddress)
		o.EmailURL = deref(emailURL)
		o.LastMessenger = deref(lastMessenger)
		o.SupportingQuote = deref(supportingQuote)
		if orderID != nil {
			o.OrderID = *orderID
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate orders: %w", err)
	}
	return out, nil
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
