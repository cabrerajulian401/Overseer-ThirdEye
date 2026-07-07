package repo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thirdeye/overseer/internal/entity"
)

const insertDiscrepancySQL = `
	INSERT INTO discrepancies (account_id, user_id, conversation_id, status,
	                           company_name, email_address, email_deeplink_url, order_id,
	                           priority, discrepancy_title, discrepancy_detail, discrepancy_quotes)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	RETURNING id, account_id, user_id, conversation_id, status,
	          company_name, email_address, email_deeplink_url, order_id,
	          priority, discrepancy_title, discrepancy_detail, discrepancy_quotes,
	          created_at, updated_at`

const listDiscrepanciesSQL = `
	SELECT id, account_id, user_id, conversation_id, status,
	       company_name, email_address, email_deeplink_url, order_id,
	       priority, discrepancy_title, discrepancy_detail, discrepancy_quotes,
	       created_at, updated_at
	FROM discrepancies
	WHERE user_id = $1
	  AND ($2::text IS NULL OR status = $2)
	ORDER BY created_at DESC` // GET /discrepancies?status= (idx_discrepancies_user)

const updateDiscrepancyStatusSQL = `
	UPDATE discrepancies SET status = $3, updated_at = now()
	WHERE user_id = $1 AND id = $2
	RETURNING id, account_id, user_id, conversation_id, status,
	          company_name, email_address, email_deeplink_url, order_id,
	          priority, discrepancy_title, discrepancy_detail, discrepancy_quotes,
	          created_at, updated_at`

// detailRow / quoteRow are the JSONB column shapes — snake_case keys, per the
// spec's Schema Updates comments. Mapping lives here and nowhere else.
type detailRow struct {
	PreviouslyAgreed string `json:"previously_agreed"`
	NewMetric        string `json:"new_metric"`
	Type             string `json:"type"`
}

type quoteRow struct {
	Quote       string `json:"quote"`
	MessageLink string `json:"message_link"`
}

type Discrepancies struct {
	db *pgxpool.Pool
}

func NewDiscrepancies(db *pgxpool.Pool) *Discrepancies { return &Discrepancies{db: db} }

// Insert stores a finding and returns the stored row so events carry the
// authoritative id/timestamps.
func (r *Discrepancies) Insert(ctx context.Context, d entity.Discrepancy) (entity.Discrepancy, error) {
	detail, err := json.Marshal(detailRow{
		PreviouslyAgreed: d.Detail.PreviouslyAgreed,
		NewMetric:        d.Detail.NewMetric,
		Type:             string(d.Detail.Type),
	})
	if err != nil {
		return entity.Discrepancy{}, fmt.Errorf("marshal discrepancy detail: %w", err)
	}
	qrs := make([]quoteRow, 0, len(d.Quotes))
	for _, q := range d.Quotes {
		qrs = append(qrs, quoteRow{Quote: q.Quote, MessageLink: q.MessageLink})
	}
	quotes, err := json.Marshal(qrs)
	if err != nil {
		return entity.Discrepancy{}, fmt.Errorf("marshal discrepancy quotes: %w", err)
	}

	rows, err := r.db.Query(ctx, insertDiscrepancySQL,
		d.AccountID, d.UserID, d.ConversationID, string(d.Status),
		d.CompanyName, nullString(d.EmailAddress), nullString(d.EmailDeeplinkURL), d.OrderID,
		string(d.Priority), d.Title, detail, quotes)
	if err != nil {
		return entity.Discrepancy{}, fmt.Errorf("insert discrepancy: %w", err)
	}
	defer rows.Close()
	out, err := scanDiscrepancies(rows)
	if err != nil {
		return entity.Discrepancy{}, err
	}
	if len(out) == 0 {
		return entity.Discrepancy{}, fmt.Errorf("insert discrepancy: no row returned")
	}
	return out[0], nil
}

func (r *Discrepancies) ListByUser(ctx context.Context, userID uuid.UUID, status *entity.DiscrepancyStatus) ([]entity.Discrepancy, error) {
	var statusStr *string
	if status != nil {
		s := string(*status)
		statusStr = &s
	}
	rows, err := r.db.Query(ctx, listDiscrepanciesSQL, userID, statusStr)
	if err != nil {
		return nil, fmt.Errorf("list discrepancies: %w", err)
	}
	defer rows.Close()
	return scanDiscrepancies(rows)
}

// UpdateStatus sets the status of the user's row; found=false when the id
// doesn't exist or belongs to another user.
func (r *Discrepancies) UpdateStatus(ctx context.Context, userID, id uuid.UUID, s entity.DiscrepancyStatus) (entity.Discrepancy, bool, error) {
	rows, err := r.db.Query(ctx, updateDiscrepancyStatusSQL, userID, id, string(s))
	if err != nil {
		return entity.Discrepancy{}, false, fmt.Errorf("update discrepancy status: %w", err)
	}
	defer rows.Close()
	out, err := scanDiscrepancies(rows)
	if err != nil {
		return entity.Discrepancy{}, false, err
	}
	if len(out) == 0 {
		return entity.Discrepancy{}, false, nil
	}
	return out[0], true, nil
}

func scanDiscrepancies(rows pgx.Rows) ([]entity.Discrepancy, error) {
	var out []entity.Discrepancy
	for rows.Next() {
		var (
			d                              entity.Discrepancy
			emailAddress, emailDeeplinkURL *string
			orderID                        *int
			status, priority               string
			detailJSON, quotesJSON         []byte
		)
		if err := rows.Scan(
			&d.ID, &d.AccountID, &d.UserID, &d.ConversationID, &status,
			&d.CompanyName, &emailAddress, &emailDeeplinkURL, &orderID,
			&priority, &d.Title, &detailJSON, &quotesJSON,
			&d.CreatedAt, &d.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan discrepancy: %w", err)
		}
		d.Status = entity.DiscrepancyStatus(status)
		d.Priority = entity.Priority(priority)
		d.EmailAddress = deref(emailAddress)
		d.EmailDeeplinkURL = deref(emailDeeplinkURL)
		if orderID != nil {
			d.OrderID = *orderID
		}
		if len(detailJSON) > 0 {
			var dr detailRow
			if err := json.Unmarshal(detailJSON, &dr); err != nil {
				return nil, fmt.Errorf("unmarshal discrepancy detail: %w", err)
			}
			d.Detail = entity.DiscrepancyDetail{
				PreviouslyAgreed: dr.PreviouslyAgreed,
				NewMetric:        dr.NewMetric,
				Type:             entity.MetricType(dr.Type),
			}
		}
		if len(quotesJSON) > 0 {
			var qs []quoteRow
			if err := json.Unmarshal(quotesJSON, &qs); err != nil {
				return nil, fmt.Errorf("unmarshal discrepancy quotes: %w", err)
			}
			for _, q := range qs {
				d.Quotes = append(d.Quotes, entity.DiscrepancyQuote{Quote: q.Quote, MessageLink: q.MessageLink})
			}
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate discrepancies: %w", err)
	}
	return out, nil
}
