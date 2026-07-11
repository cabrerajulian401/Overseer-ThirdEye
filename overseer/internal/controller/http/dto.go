package http

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/thirdeye/overseer/internal/entity"
)

// DTOs: camelCase JSON on the wire, mapped from tag-less entities here and
// nowhere else.

type orderDTO struct {
	ID              string  `json:"id"`
	ConversationID  string  `json:"conversationId"`
	CompanyName     string  `json:"companyName"`
	EmailAddress    string  `json:"emailAddress,omitempty"`
	OrderID         int     `json:"orderId"`
	EmailURL        string  `json:"emailUrl,omitempty"`
	Status          string  `json:"status"`
	DateCreated     string  `json:"dateCreated"`
	OriginalEstDate *string `json:"originalEstDate"`
	CurrentEstDate  *string `json:"currentEstDate"`
	LastMessenger   string  `json:"lastMessenger,omitempty"`
	SupportingQuote string  `json:"supportingQuote,omitempty"`
}

type listOrdersResponse struct {
	Orders     []orderDTO `json:"orders"`
	NextCursor *string    `json:"nextCursor"` // opaque; null when no more pages
}

type syncResponse struct {
	SyncID string `json:"syncId"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type discrepancyDetailDTO struct {
	PreviouslyAgreed string `json:"previouslyAgreed"`
	NewMetric        string `json:"newMetric"`
	Type             string `json:"type"`
}

type discrepancyQuoteDTO struct {
	Quote       string `json:"quote"`
	MessageLink string `json:"messageLink,omitempty"`
}

type discrepancyDTO struct {
	ID               string                `json:"id"`
	ConversationID   string                `json:"conversationId"`
	Status           string                `json:"status"`
	CompanyName      string                `json:"companyName"`
	EmailAddress     string                `json:"emailAddress,omitempty"`
	EmailDeeplinkURL string                `json:"emailDeeplinkUrl,omitempty"`
	OrderID          int                   `json:"orderId"`
	Priority         string                `json:"priority"`
	Title            string                `json:"title"`
	Detail           discrepancyDetailDTO  `json:"detail"`
	Quotes           []discrepancyQuoteDTO `json:"quotes"`
	DateCreated      string                `json:"dateCreated"`
}

type listDiscrepanciesResponse struct {
	Discrepancies []discrepancyDTO `json:"discrepancies"`
}

type patchDiscrepancyRequest struct {
	Status string `json:"status"`
}

func toDiscrepancyDTO(d entity.Discrepancy) discrepancyDTO {
	quotes := make([]discrepancyQuoteDTO, 0, len(d.Quotes))
	for _, q := range d.Quotes {
		quotes = append(quotes, discrepancyQuoteDTO{Quote: q.Quote, MessageLink: q.MessageLink})
	}
	return discrepancyDTO{
		ID:               d.ID.String(),
		ConversationID:   d.ConversationID,
		Status:           string(d.Status),
		CompanyName:      d.CompanyName,
		EmailAddress:     d.EmailAddress,
		EmailDeeplinkURL: d.EmailDeeplinkURL,
		OrderID:          d.OrderID,
		Priority:         string(d.Priority),
		Title:            d.Title,
		Detail: discrepancyDetailDTO{
			PreviouslyAgreed: d.Detail.PreviouslyAgreed,
			NewMetric:        d.Detail.NewMetric,
			Type:             string(d.Detail.Type),
		},
		Quotes:      quotes,
		DateCreated: d.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// extractedFieldsDTO is the confirm card's shape, used in both directions:
// the scan response carries the raw extraction; the record request carries
// the user-edited version.
type extractedFieldsDTO struct {
	CompanyName     string  `json:"companyName"`
	EmailAddress    string  `json:"emailAddress,omitempty"`
	OrderID         int     `json:"orderId"`
	Status          string  `json:"status,omitempty"`
	OriginalEstDate *string `json:"originalEstDate"`
	CurrentEstDate  *string `json:"currentEstDate"`
	LastMessenger   string  `json:"lastMessenger,omitempty"`
	SupportingQuote string  `json:"supportingQuote,omitempty"`
}

type destinationDTO struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	WebURL string `json:"webUrl"`
}

type scanRequest struct {
	MessageID      string `json:"messageId"`
	ConversationID string `json:"conversationId"`
}

type scanResponse struct {
	ScanID          string             `json:"scanId"`
	ExtractedFields extractedFieldsDTO `json:"extractedFields"`
	Destinations    []destinationDTO   `json:"destinations"`
}

type recordRequest struct {
	SelectedResource string             `json:"selectedResource"`
	Fields           extractedFieldsDTO `json:"fields"`
}

type recordResponse struct {
	WebURL string `json:"webUrl"`
	Status string `json:"status"`
}

func toExtractedFieldsDTO(f entity.OrderExtraction) extractedFieldsDTO {
	return extractedFieldsDTO{
		CompanyName:     f.CompanyName,
		EmailAddress:    f.EmailAddress,
		OrderID:         f.OrderID,
		Status:          string(f.Status),
		OriginalEstDate: dateString(f.OriginalEstDate),
		CurrentEstDate:  dateString(f.CurrentEstDate),
		LastMessenger:   f.LastMessenger,
		SupportingQuote: f.SupportingQuote,
	}
}

// fromExtractedFieldsDTO parses the user-approved fields; bad dates are a
// 400, not a silent nil.
func fromExtractedFieldsDTO(d extractedFieldsDTO) (entity.OrderExtraction, error) {
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
	original, err := parse(d.OriginalEstDate)
	if err != nil {
		return entity.OrderExtraction{}, fmt.Errorf("originalEstDate: %w", err)
	}
	current, err := parse(d.CurrentEstDate)
	if err != nil {
		return entity.OrderExtraction{}, fmt.Errorf("currentEstDate: %w", err)
	}
	return entity.OrderExtraction{
		CompanyName:     d.CompanyName,
		EmailAddress:    d.EmailAddress,
		OrderID:         d.OrderID,
		Status:          entity.OrderStatus(d.Status),
		OriginalEstDate: original,
		CurrentEstDate:  current,
		LastMessenger:   d.LastMessenger,
		SupportingQuote: d.SupportingQuote,
	}, nil
}

// graphNotification mirrors one entry of Graph's change-notification POST.
type graphNotification struct {
	SubscriptionID string `json:"subscriptionId"`
	ChangeType     string `json:"changeType"`
	ClientState    string `json:"clientState"`
	ResourceData   struct {
		ID string `json:"id"` // Graph message id
	} `json:"resourceData"`
}

type graphNotificationPayload struct {
	Value []graphNotification `json:"value"`
}

type subscriptionCreatedResponse struct {
	SubscriptionID string `json:"subscriptionId"`
	ExpiresAt      string `json:"expiresAt"`
}

func toOrderDTO(o entity.Order) orderDTO {
	return orderDTO{
		ID:              o.ID.String(),
		ConversationID:  o.ConversationID,
		CompanyName:     o.CompanyName,
		EmailAddress:    o.EmailAddress,
		OrderID:         o.OrderID,
		EmailURL:        o.EmailURL,
		Status:          string(o.Status),
		DateCreated:     o.CreatedAt.UTC().Format(time.RFC3339),
		OriginalEstDate: dateString(o.OriginalEstDate),
		CurrentEstDate:  dateString(o.CurrentEstDate),
		LastMessenger:   o.LastMessenger,
		SupportingQuote: o.SupportingQuote,
	}
}

func dateString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format("2006-01-02")
	return &s
}

// Cursors are opaque to clients: base64 of the boundary row's created_at.
func encodeCursor(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano)))
	return &s
}

func decodeCursor(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339Nano, string(raw))
	if err != nil {
		return nil, err
	}
	return &t, nil
}
