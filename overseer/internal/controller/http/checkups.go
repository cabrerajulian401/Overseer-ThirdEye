package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/usecase/checkups"
)

// CheckupsController is transport only: parse → call usecase → map to DTO.
type CheckupsController struct {
	Create *checkups.CreateCheckupMessages
	List   *checkups.ListCheckups
	Update *checkups.UpdateStatus

	Log interface {
		Error(msg string, args ...any)
	}
}

// POST /checkups — create a follow-up flow. The cadence expands to one row
// per message; every id is server-minted and returned in the response.
func (h *CheckupsController) PostCheckups(c echo.Context) error {
	auth := authFrom(c)
	accountID, errA := uuid.Parse(auth.AccountID)
	userID, errU := uuid.Parse(auth.UserID)
	if errA != nil || errU != nil {
		return c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid auth context"})
	}

	var req createCheckupRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid request body"})
	}
	if req.CompanyName == "" || req.EmailAddress == "" || req.Subject == "" || req.Body == "" || req.SendDate == "" {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "companyName, emailAddress, subject, body and sendDate are required"})
	}
	sendDate, err := time.Parse(time.RFC3339, req.SendDate)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "sendDate must be RFC3339"})
	}
	var estDate *time.Time
	if req.CurrentEstDate != nil && *req.CurrentEstDate != "" {
		t, err := time.Parse("2006-01-02", *req.CurrentEstDate)
		if err != nil {
			return c.JSON(http.StatusBadRequest, errorResponse{Error: "currentEstDate must be YYYY-MM-DD"})
		}
		estDate = &t
	}
	if req.RepeatFlow > 1 && req.RepeatEveryDays <= 0 {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "repeatEveryDays must be positive when repeatFlow > 1"})
	}

	stored, err := h.Create.Execute(c.Request().Context(), checkups.CreateInput{
		AccountID:      accountID,
		UserID:         userID,
		ConversationID: req.ConversationID,
		CompanyName:    req.CompanyName,
		EmailAddress:   req.EmailAddress,
		OrderID:        req.OrderID,
		Subject:        req.Subject,
		Body:           req.Body,
		SendDate:       sendDate,
		CurrentEstDate: estDate,
		CancelIfReply:  req.CancelIfReply,
		RepeatFlow:     req.RepeatFlow,
		RepeatEvery:    time.Duration(req.RepeatEveryDays) * 24 * time.Hour,
	})
	if err != nil {
		h.Log.Error("create checkups failed", "error", err)
		return c.JSON(http.StatusInternalServerError, errorResponse{Error: "failed to create checkups"})
	}

	dtos := make([]checkupDTO, 0, len(stored))
	for _, m := range stored {
		dtos = append(dtos, toCheckupDTO(m))
	}
	return c.JSON(http.StatusCreated, createCheckupResponse{FlowID: stored[0].FlowID.String(), Checkups: dtos})
}

// GET /checkups?status= — the reload path: the add-in repopulates its list
// from the DB on refresh.
func (h *CheckupsController) GetCheckups(c echo.Context) error {
	auth := authFrom(c)
	userID, err := uuid.Parse(auth.UserID)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid auth context"})
	}

	in := checkups.ListInput{UserID: userID}
	if s := c.QueryParam("status"); s != "" {
		status := entity.CheckupStatus(s)
		if !entity.ValidCheckupStatus(status) {
			return c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid status"})
		}
		in.Status = &status
	}

	out, err := h.List.Execute(c.Request().Context(), in)
	if err != nil {
		h.Log.Error("list checkups failed", "error", err)
		return c.JSON(http.StatusInternalServerError, errorResponse{Error: "failed to list checkups"})
	}

	dtos := make([]checkupDTO, 0, len(out))
	for _, m := range out {
		dtos = append(dtos, toCheckupDTO(m))
	}
	return c.JSON(http.StatusOK, listCheckupsResponse{Checkups: dtos})
}

// PATCH /checkups/{checkupId} — pause, resume, or cancel one message row.
// Clients may only request pending, paused, or cancelled; sent and sending
// belong to the scheduler.
func (h *CheckupsController) PatchCheckup(c echo.Context) error {
	auth := authFrom(c)
	userID, err := uuid.Parse(auth.UserID)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid auth context"})
	}
	id, err := uuid.Parse(c.Param("checkupId"))
	if err != nil {
		return c.JSON(http.StatusNotFound, errorResponse{Error: "checkup not found"})
	}

	var req patchCheckupRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid request body"})
	}
	status := entity.CheckupStatus(req.Status)
	if status != entity.CheckupPending && status != entity.CheckupPaused && status != entity.CheckupCancelled {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "status must be pending, paused, or cancelled"})
	}

	m, err := h.Update.Execute(c.Request().Context(), userID, id, status)
	if err != nil {
		switch {
		case errors.Is(err, checkups.ErrCheckupNotFound):
			return c.JSON(http.StatusNotFound, errorResponse{Error: "checkup not found"})
		case errors.Is(err, checkups.ErrIllegalTransition):
			return c.JSON(http.StatusConflict, errorResponse{Error: "checkup is sent, cancelled, or mid-send"})
		default:
			h.Log.Error("update checkup failed", "checkupId", id, "error", err)
			return c.JSON(http.StatusInternalServerError, errorResponse{Error: "failed to update checkup"})
		}
	}
	return c.JSON(http.StatusOK, toCheckupDTO(m))
}
