package http

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/thirdeye/overseer/internal/usecase/subscriptions"
)

// SubscriptionsController is transport only: parse → call usecase → DTO.
type SubscriptionsController struct {
	Ensure *subscriptions.EnsureSubscription

	Log interface {
		Error(msg string, args ...any)
	}
}

// POST /subscriptions — register the Graph change-notification subscription
// for the authed user (called once after onboarding; re-running replaces it).
func (h *SubscriptionsController) PostSubscriptions(c echo.Context) error {
	auth := authFrom(c)
	userID, err := uuid.Parse(auth.UserID)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid auth context"})
	}

	out, err := h.Ensure.Execute(c.Request().Context(), subscriptions.EnsureInput{
		UserID:     userID,
		GraphToken: auth.GraphToken,
	})
	if err != nil {
		h.Log.Error("create subscription failed", "error", err)
		return c.JSON(http.StatusBadGateway, errorResponse{Error: "failed to create graph subscription"})
	}

	return c.JSON(http.StatusCreated, subscriptionCreatedResponse{
		SubscriptionID: out.GraphSubscriptionID,
		ExpiresAt:      out.ExpiresAt.UTC().Format(time.RFC3339),
	})
}
