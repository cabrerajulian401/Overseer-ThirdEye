package http

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/usecase/orders"
)

// OrdersController is transport only: parse → call usecase → map to DTO.
type OrdersController struct {
	List  *orders.ListOrders
	Build *orders.BuildActiveOrderList

	// SyncLookback returns how far back the onboarding sync reads the inbox
	// (injected so tests control the clock).
	SyncLookback func() time.Time

	// Dispatch runs the sync job off the request path. Production wiring is
	// `go fn()`; E2E tests inject a synchronous version.
	Dispatch func(fn func())

	Log interface {
		Error(msg string, args ...any)
	}
}

// GET /orders?status&cursor&limit — read-only, serves reloads from the
// orders table.
func (h *OrdersController) GetOrders(c echo.Context) error {
	auth := authFrom(c)
	userID, err := uuid.Parse(auth.UserID)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid auth context"})
	}

	in := orders.ListInput{UserID: userID}

	if s := c.QueryParam("status"); s != "" {
		status := entity.OrderStatus(s)
		if !entity.ValidOrderStatus(status) {
			return c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid status"})
		}
		in.Status = &status
	}
	if cur := c.QueryParam("cursor"); cur != "" {
		t, err := decodeCursor(cur)
		if err != nil {
			return c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid cursor"})
		}
		in.Cursor = t
	}
	if l := c.QueryParam("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n <= 0 {
			return c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid limit"})
		}
		in.Limit = n
	}

	out, err := h.List.Execute(c.Request().Context(), in)
	if err != nil {
		h.Log.Error("list orders failed", "error", err)
		return c.JSON(http.StatusInternalServerError, errorResponse{Error: "failed to list orders"})
	}

	dtos := make([]orderDTO, 0, len(out.Orders))
	for _, o := range out.Orders {
		dtos = append(dtos, toOrderDTO(o))
	}
	return c.JSON(http.StatusOK, listOrdersResponse{Orders: dtos, NextCursor: encodeCursor(out.NextCursor)})
}

// POST /sync — one-time onboarding inbox sync. Kicks off the Graph fetch +
// LLM formatting as an async job (a month of messages through an LLM can take
// minutes; don't hold the request open). Returns 202 {syncId}; completion is
// pushed over the realtime channel as sync.completed.
func (h *OrdersController) PostSync(c echo.Context) error {
	auth := authFrom(c)
	accountID, errA := uuid.Parse(auth.AccountID)
	userID, errU := uuid.Parse(auth.UserID)
	if errA != nil || errU != nil {
		return c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid auth context"})
	}

	in := orders.BuildInput{
		SyncID:     uuid.New(), // server mints all IDs
		AccountID:  accountID,
		UserID:     userID,
		GraphToken: auth.GraphToken,
		Since:      h.SyncLookback(),
	}

	h.Dispatch(func() {
		// Detached from the request context: the job outlives the response.
		if _, err := h.Build.Execute(context.Background(), in); err != nil {
			h.Log.Error("sync job failed", "syncId", in.SyncID, "error", err)
		}
	})

	return c.JSON(http.StatusAccepted, syncResponse{SyncID: in.SyncID.String()})
}
