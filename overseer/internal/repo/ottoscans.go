package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thirdeye/overseer/internal/entity"
)

const insertOttoScanSQL = `
	INSERT INTO otto_scans (account_id, user_id, message_id, conversation_id,
	                        company_name, email_address, order_id, email_url,
	                        extracted_fields, status)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	RETURNING id, account_id, user_id, message_id, conversation_id,
	          email_url, extracted_fields, selected_resource, status,
	          created_at, updated_at`

const getOttoScanSQL = `
	SELECT id, account_id, user_id, message_id, conversation_id,
	       email_url, extracted_fields, selected_resource, status,
	       created_at, updated_at
	FROM otto_scans
	WHERE user_id = $1 AND id = $2`

const markRecordedSQL = `
	UPDATE otto_scans SET status = 'recorded', selected_resource = $3, updated_at = now()
	WHERE user_id = $1 AND id = $2`

// fieldsRow is the extracted_fields JSONB shape — snake_case keys, dates as
// YYYY-MM-DD strings. Mapping lives here and nowhere else.
type fieldsRow struct {
	CompanyName     string  `json:"company_name"`
	EmailAddress    string  `json:"email_address"`
	OrderID         int     `json:"order_id"`
	Status          string  `json:"status"`
	OriginalEstDate *string `json:"original_est_date"`
	CurrentEstDate  *string `json:"current_est_date"`
	LastMessenger   string  `json:"last_messenger"`
	SupportingQuote string  `json:"supporting_quote"`
}

func toFieldsRow(f entity.OrderExtraction) fieldsRow {
	dateStr := func(t *time.Time) *string {
		if t == nil {
			return nil
		}
		s := t.Format("2006-01-02")
		return &s
	}
	return fieldsRow{
		CompanyName:     f.CompanyName,
		EmailAddress:    f.EmailAddress,
		OrderID:         f.OrderID,
		Status:          string(f.Status),
		OriginalEstDate: dateStr(f.OriginalEstDate),
		CurrentEstDate:  dateStr(f.CurrentEstDate),
		LastMessenger:   f.LastMessenger,
		SupportingQuote: f.SupportingQuote,
	}
}

func fromFieldsRow(fr fieldsRow) (entity.OrderExtraction, error) {
	parse := func(s *string) (*time.Time, error) {
		if s == nil || *s == "" {
			return nil, nil
		}
		t, err := time.Parse("2006-01-02", *s)
		if err != nil {
			return nil, err
		}
		return &t, nil
	}
	original, err := parse(fr.OriginalEstDate)
	if err != nil {
		return entity.OrderExtraction{}, fmt.Errorf("original_est_date: %w", err)
	}
	current, err := parse(fr.CurrentEstDate)
	if err != nil {
		return entity.OrderExtraction{}, fmt.Errorf("current_est_date: %w", err)
	}
	return entity.OrderExtraction{
		CompanyName:     fr.CompanyName,
		EmailAddress:    fr.EmailAddress,
		OrderID:         fr.OrderID,
		Status:          entity.OrderStatus(fr.Status),
		OriginalEstDate: original,
		CurrentEstDate:  current,
		LastMessenger:   fr.LastMessenger,
		SupportingQuote: fr.SupportingQuote,
	}, nil
}

type OttoScans struct {
	db *pgxpool.Pool
}

func NewOttoScans(db *pgxpool.Pool) *OttoScans { return &OttoScans{db: db} }

// Insert stores a scan and returns the stored row (server-minted id and
// timestamps). Identity columns are denormalized from Fields.
func (r *OttoScans) Insert(ctx context.Context, s entity.OttoScan) (entity.OttoScan, error) {
	fields, err := json.Marshal(toFieldsRow(s.Fields))
	if err != nil {
		return entity.OttoScan{}, fmt.Errorf("marshal extracted fields: %w", err)
	}

	var orderID *int
	if s.Fields.OrderID != 0 {
		orderID = &s.Fields.OrderID
	}
	rows, err := r.db.Query(ctx, insertOttoScanSQL,
		s.AccountID, s.UserID, s.MessageID, s.ConversationID,
		nullString(s.Fields.CompanyName), nullString(s.Fields.EmailAddress), orderID, nullString(s.EmailURL),
		fields, string(s.Status))
	if err != nil {
		return entity.OttoScan{}, fmt.Errorf("insert otto scan: %w", err)
	}
	defer rows.Close()
	out, err := scanOttoScans(rows)
	if err != nil {
		return entity.OttoScan{}, err
	}
	if len(out) == 0 {
		return entity.OttoScan{}, fmt.Errorf("insert otto scan: no row returned")
	}
	return out[0], nil
}

// GetByID returns the user's scan; found=false when the id doesn't exist or
// belongs to another user.
func (r *OttoScans) GetByID(ctx context.Context, userID, id uuid.UUID) (entity.OttoScan, bool, error) {
	rows, err := r.db.Query(ctx, getOttoScanSQL, userID, id)
	if err != nil {
		return entity.OttoScan{}, false, fmt.Errorf("get otto scan: %w", err)
	}
	defer rows.Close()
	out, err := scanOttoScans(rows)
	if err != nil {
		return entity.OttoScan{}, false, err
	}
	if len(out) == 0 {
		return entity.OttoScan{}, false, nil
	}
	return out[0], true, nil
}

// MarkRecorded sets status='recorded' and the chosen resource; erroring when
// no owned row matched keeps the fake and the SQL honest with each other.
func (r *OttoScans) MarkRecorded(ctx context.Context, userID, id uuid.UUID, selectedResource string) error {
	tag, err := r.db.Exec(ctx, markRecordedSQL, userID, id, selectedResource)
	if err != nil {
		return fmt.Errorf("mark scan recorded: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark scan recorded: no scan %s for user", id)
	}
	return nil
}

func scanOttoScans(rows pgx.Rows) ([]entity.OttoScan, error) {
	var out []entity.OttoScan
	for rows.Next() {
		var (
			s                          entity.OttoScan
			emailURL, selectedResource *string
			fieldsJSON                 []byte
			status                     string
		)
		if err := rows.Scan(
			&s.ID, &s.AccountID, &s.UserID, &s.MessageID, &s.ConversationID,
			&emailURL, &fieldsJSON, &selectedResource, &status,
			&s.CreatedAt, &s.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan otto scan: %w", err)
		}
		s.Status = entity.ScanStatus(status)
		s.EmailURL = deref(emailURL)
		s.SelectedResource = deref(selectedResource)
		if len(fieldsJSON) > 0 {
			var fr fieldsRow
			if err := json.Unmarshal(fieldsJSON, &fr); err != nil {
				return nil, fmt.Errorf("unmarshal extracted fields: %w", err)
			}
			fields, err := fromFieldsRow(fr)
			if err != nil {
				return nil, err
			}
			s.Fields = fields
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate otto scans: %w", err)
	}
	return out, nil
}
