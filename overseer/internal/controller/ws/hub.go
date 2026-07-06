// Package ws serves GET /ws — the single realtime channel. One connection,
// typed events: { "type": "order.updated" | "sync.completed" | ..., "data": {...} }.
// New features add event types, never new sockets.
package ws

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"

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
	default:
		return d
	}
}
