// Package ws serves GET /ws — the single realtime channel. One connection,
// typed events: { "type": "order.updated" | "sync.completed" | ..., "data": {...} }.
// New features add event types, never new sockets.
package ws

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"

	"github.com/thirdeye/overseer/internal/entity"
)

// wireEvent is the camelCase JSON envelope pushed to clients.
type wireEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

type syncCompletedDTO struct {
	SyncID         string `json:"syncId"`
	OrdersUpserted int    `json:"ordersUpserted"`
}

// orderDTO is order.updated's wire shape — same camelCase fields the HTTP
// layer serves, mapped here because each edge owns its own DTOs.
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

type discrepancyDetailDTO struct {
	PreviouslyAgreed string `json:"previouslyAgreed"`
	NewMetric        string `json:"newMetric"`
	Type             string `json:"type"`
}

type discrepancyQuoteDTO struct {
	Quote       string `json:"quote"`
	MessageLink string `json:"messageLink,omitempty"`
}

// discrepancyDTO is discrepancy.found's wire shape — same camelCase fields
// the HTTP layer serves, mapped here because each edge owns its own DTOs.
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

func dateString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format("2006-01-02")
	return &s
}

type Hub struct {
	log      *slog.Logger
	upgrader websocket.Upgrader

	mu      sync.Mutex
	clients map[*websocket.Conn]struct{}
}

func NewHub(log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{
		log: log,
		upgrader: websocket.Upgrader{
			// MVP: the add-in runs inside Outlook, so origins vary; auth is
			// the bearer-token middleware in front of this handler.
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients: make(map[*websocket.Conn]struct{}),
	}
}

// Handler upgrades GET /ws and keeps the connection registered until the
// client goes away. Clients only listen; inbound frames are discarded.
func (h *Hub) Handler(c echo.Context) error {
	conn, err := h.upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}

	h.mu.Lock()
	h.clients[conn] = struct{}{}
	h.mu.Unlock()

	go func() {
		defer h.drop(conn)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	return nil
}

// Clients reports how many connections are currently registered. Registration
// lands a beat AFTER the client sees the 101 — anyone publishing right after
// a connect (tests especially) must await it, or the broadcast is lost.
func (h *Hub) Clients() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// Publish implements the usecases' EventPublisher port: broadcast the event
// to every connected client. Safe to call with zero clients.
func (h *Hub) Publish(e entity.Event) {
	payload, err := json.Marshal(wireEvent{Type: string(e.Type), Data: toDTO(e.Data)})
	if err != nil {
		h.log.Error("ws: marshal event", "type", e.Type, "error", err)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for conn := range h.clients {
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			conn.Close()
			delete(h.clients, conn)
		}
	}
}

func (h *Hub) drop(conn *websocket.Conn) {
	conn.Close()
	h.mu.Lock()
	delete(h.clients, conn)
	h.mu.Unlock()
}

// toDTO maps entity payloads to their camelCase wire shapes.
func toDTO(data any) any {
	switch d := data.(type) {
	case entity.SyncCompleted:
		return syncCompletedDTO{SyncID: d.SyncID.String(), OrdersUpserted: d.OrdersUpserted}
	case entity.Order:
		return orderDTO{
			ID:              d.ID.String(),
			ConversationID:  d.ConversationID,
			CompanyName:     d.CompanyName,
			EmailAddress:    d.EmailAddress,
			OrderID:         d.OrderID,
			EmailURL:        d.EmailURL,
			Status:          string(d.Status),
			DateCreated:     d.CreatedAt.UTC().Format(time.RFC3339),
			OriginalEstDate: dateString(d.OriginalEstDate),
			CurrentEstDate:  dateString(d.CurrentEstDate),
			LastMessenger:   d.LastMessenger,
			SupportingQuote: d.SupportingQuote,
		}
	case entity.Discrepancy:
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
	default:
		return d
	}
}
