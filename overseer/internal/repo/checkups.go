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

const checkupColumns = `id, account_id, user_id, flow_id, conversation_id,
	       company_name, email_address, order_id, status, subject, body,
	       send_date, current_est_date, cancel_if_reply, repeat_flow,
	       created_at, updated_at`

const insertCheckupSQL = `
	INSERT INTO checkup_messages (account_id, user_id, flow_id, conversation_id,
	                              company_name, email_address, order_id, status,
	                              subject, body, send_date, current_est_date,
	                              cancel_if_reply, repeat_flow)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	RETURNING ` + checkupColumns

const claimDueCheckupsSQL = `
	UPDATE checkup_messages SET status = 'sending', updated_at = now()
	WHERE id IN (
		SELECT id FROM checkup_messages
		WHERE status = 'pending' AND send_date <= $1   -- <=, never = (uses idx_checkups_due)
		FOR UPDATE SKIP LOCKED
	)
	RETURNING ` + checkupColumns // two workers can't double-send

const markCheckupSentSQL = `
	UPDATE checkup_messages SET status = 'sent', updated_at = now()
	WHERE id = $1 AND status = 'sending'`

const releaseCheckupSQL = `
	UPDATE checkup_messages SET status = 'pending', updated_at = now()
	WHERE id = $1 AND status = 'sending'`

const getCheckupSQL = `
	SELECT ` + checkupColumns + `
	FROM checkup_messages
	WHERE user_id = $1 AND id = $2`

const updateCheckupStatusSQL = `
	UPDATE checkup_messages SET status = $3, updated_at = now()
	WHERE user_id = $1 AND id = $2
	RETURNING ` + checkupColumns

const listCheckupsSQL = `
	SELECT ` + checkupColumns + `
	FROM checkup_messages
	WHERE user_id = $1 AND ($2::text IS NULL OR status = $2)
	ORDER BY send_date ASC`

const cancelCheckupsByConversationSQL = `
	UPDATE checkup_messages SET status = 'cancelled', updated_at = now()
	WHERE user_id = $1 AND conversation_id = $2
	  AND status = 'pending' AND cancel_if_reply` // HandleChangeMessage's reply-detection hook

type Checkups struct {
	db *pgxpool.Pool
}

func NewCheckups(db *pgxpool.Pool) *Checkups { return &Checkups{db: db} }

// InsertFlow stores one flow's messages atomically (all rows or none) and
// returns the stored rows with server-minted ids and timestamps.
func (r *Checkups) InsertFlow(ctx context.Context, msgs []entity.CheckupMessage) ([]entity.CheckupMessage, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin insert checkup flow: %w", err)
	}
	defer tx.Rollback(ctx)

	out := make([]entity.CheckupMessage, 0, len(msgs))
	for _, m := range msgs {
		var orderID *int
		if m.OrderID != 0 {
			orderID = &m.OrderID
		}
		var repeat *int
		if m.RepeatFlow != 0 {
			repeat = &m.RepeatFlow
		}
		rows, err := tx.Query(ctx, insertCheckupSQL,
			m.AccountID, m.UserID, m.FlowID, nullString(m.ConversationID),
			m.CompanyName, m.EmailAddress, orderID, string(m.Status),
			m.Subject, m.Body, m.SendDate, m.CurrentEstDate,
			m.CancelIfReply, repeat)
		if err != nil {
			return nil, fmt.Errorf("insert checkup message: %w", err)
		}
		stored, err := scanCheckups(rows)
		if err != nil {
			return nil, err
		}
		if len(stored) == 0 {
			return nil, fmt.Errorf("insert checkup message: no row returned")
		}
		out = append(out, stored[0])
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit insert checkup flow: %w", err)
	}
	return out, nil
}

// ClaimDue atomically flips pending rows with send_date <= now to 'sending'
// and returns them.
func (r *Checkups) ClaimDue(ctx context.Context, now time.Time) ([]entity.CheckupMessage, error) {
	rows, err := r.db.Query(ctx, claimDueCheckupsSQL, now)
	if err != nil {
		return nil, fmt.Errorf("claim due checkups: %w", err)
	}
	return scanCheckups(rows)
}

// MarkSent finishes a claimed row; erroring when no claimed row matched keeps
// the fake and the SQL honest with each other.
func (r *Checkups) MarkSent(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, markCheckupSentSQL, id)
	if err != nil {
		return fmt.Errorf("mark checkup sent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark checkup sent: no claimed row %s", id)
	}
	return nil
}

// Release returns a claimed row to 'pending' so the next tick retries it.
func (r *Checkups) Release(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, releaseCheckupSQL, id)
	if err != nil {
		return fmt.Errorf("release checkup: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("release checkup: no claimed row %s", id)
	}
	return nil
}

// GetByID returns the user's row; found=false when the id doesn't exist or
// belongs to another user.
func (r *Checkups) GetByID(ctx context.Context, userID, id uuid.UUID) (entity.CheckupMessage, bool, error) {
	rows, err := r.db.Query(ctx, getCheckupSQL, userID, id)
	if err != nil {
		return entity.CheckupMessage{}, false, fmt.Errorf("get checkup: %w", err)
	}
	out, err := scanCheckups(rows)
	if err != nil {
		return entity.CheckupMessage{}, false, err
	}
	if len(out) == 0 {
		return entity.CheckupMessage{}, false, nil
	}
	return out[0], true, nil
}

// UpdateStatus sets the row's status; found=false when no owned row matched.
// Transition legality is the usecase's decision, not the repo's.
func (r *Checkups) UpdateStatus(ctx context.Context, userID, id uuid.UUID, s entity.CheckupStatus) (entity.CheckupMessage, bool, error) {
	rows, err := r.db.Query(ctx, updateCheckupStatusSQL, userID, id, string(s))
	if err != nil {
		return entity.CheckupMessage{}, false, fmt.Errorf("update checkup status: %w", err)
	}
	out, err := scanCheckups(rows)
	if err != nil {
		return entity.CheckupMessage{}, false, err
	}
	if len(out) == 0 {
		return entity.CheckupMessage{}, false, nil
	}
	return out[0], true, nil
}

// ListByUser returns the user's rows, soonest send_date first; nil status = all.
func (r *Checkups) ListByUser(ctx context.Context, userID uuid.UUID, status *entity.CheckupStatus) ([]entity.CheckupMessage, error) {
	var s *string
	if status != nil {
		v := string(*status)
		s = &v
	}
	rows, err := r.db.Query(ctx, listCheckupsSQL, userID, s)
	if err != nil {
		return nil, fmt.Errorf("list checkups: %w", err)
	}
	return scanCheckups(rows)
}

// CancelPendingByConversation cancels the user's pending cancel_if_reply rows
// on a conversation and returns how many were cancelled. Idempotent — a Graph
// redelivery finds nothing left to cancel.
func (r *Checkups) CancelPendingByConversation(ctx context.Context, userID uuid.UUID, conversationID string) (int, error) {
	tag, err := r.db.Exec(ctx, cancelCheckupsByConversationSQL, userID, conversationID)
	if err != nil {
		return 0, fmt.Errorf("cancel checkups by conversation: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func scanCheckups(rows pgx.Rows) ([]entity.CheckupMessage, error) {
	defer rows.Close()
	var out []entity.CheckupMessage
	for rows.Next() {
		var (
			m              entity.CheckupMessage
			conversationID *string
			orderID        *int
			status         string
			estDate        *time.Time
			repeat         *int
		)
		if err := rows.Scan(
			&m.ID, &m.AccountID, &m.UserID, &m.FlowID, &conversationID,
			&m.CompanyName, &m.EmailAddress, &orderID, &status, &m.Subject, &m.Body,
			&m.SendDate, &estDate, &m.CancelIfReply, &repeat,
			&m.CreatedAt, &m.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan checkup: %w", err)
		}
		m.ConversationID = deref(conversationID)
		m.Status = entity.CheckupStatus(status)
		m.CurrentEstDate = estDate
		if orderID != nil {
			m.OrderID = *orderID
		}
		if repeat != nil {
			m.RepeatFlow = *repeat
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate checkups: %w", err)
	}
	return out, nil
}
