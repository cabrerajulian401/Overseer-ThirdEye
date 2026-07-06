package http

import (
	"encoding/base64"
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
