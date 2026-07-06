package http

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/thirdeye/overseer/internal/usecase/messages"
)

// WebhooksController receives Graph change notifications. It is NOT behind
// the auth middleware — Graph sends no bearer token; notifications are
// authenticated by the clientState shared secret instead.
type WebhooksController struct {
	Handle *messages.HandleChangeMessage

	// ClientState must match what CreateSubscription registered; anything
	// else is dropped as spoofed.
	ClientState string

	// Dispatch runs jobs off the request path (production: `go fn()`).
	// Never do Graph/LLM work inside the webhook handler.
	Dispatch func(fn func())

	Log interface {
		Error(msg string, args ...any)
		Warn(msg string, args ...any)
		Info(msg string, args ...any)
	}
}

// POST /webhooks/graph — two contracts, one endpoint:
//   - subscription validation: echo validationToken as text/plain 200 within 10s
//   - notifications: enqueue and return 202 immediately
func (h *WebhooksController) PostGraph(c echo.Context) error {
	if token := c.QueryParam("validationToken"); token != "" {
		return c.String(http.StatusOK, token)
	}

	var payload graphNotificationPayload
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid notification payload"})
	}

	for _, n := range payload.Value {
		if n.ClientState != h.ClientState {
			h.Log.Warn("webhook: clientState mismatch, dropping notification",
				"subscriptionId", n.SubscriptionID)
			continue
		}
		in := messages.HandleInput{SubscriptionID: n.SubscriptionID, MessageID: n.ResourceData.ID}
		h.Dispatch(func() {
			// Detached from the request context: the job outlives the 202.
			outcome, err := h.Handle.Execute(context.Background(), in)
			if err != nil {
				h.Log.Error("handle change message failed", "messageId", in.MessageID, "error", err)
				return
			}
			h.Log.Info("change message handled", "messageId", in.MessageID, "outcome", string(outcome))
		})
	}
	return c.NoContent(http.StatusAccepted)
}
