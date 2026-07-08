package http

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/usecase/orders"
)

// DiscrepanciesController is transport only: parse → call usecase → map to DTO.
type DiscrepanciesController struct {
	List   *orders.ListDiscrepancies
	Update *orders.UpdateDiscrepancyStatus

	Log interface {
		Error(msg string, args ...any)
	}
}

// GET /discrepancies?status= — the reload path: discrepancy.found reaches
// only connected clients, so the DB serves everyone else.
func (h *DiscrepanciesController) GetDiscrepancies(c echo.Context) error {
	auth := authFrom(c)
	userID, err := uuid.Parse(auth.UserID)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid auth context"})
	}

	in := orders.ListDiscrepanciesInput{UserID: userID}
	if s := c.QueryParam("status"); s != "" {
		status := entity.DiscrepancyStatus(s)
		if !entity.ValidDiscrepancyStatus(status) {
			return c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid status"})
		}
		in.Status = &status
	}

	out, err := h.List.Execute(c.Request().Context(), in)
	if err != nil {
		h.Log.Error("list discrepancies failed", "error", err)
		return c.JSON(http.StatusInternalServerError, errorResponse{Error: "failed to list discrepancies"})
	}

	dtos := make([]discrepancyDTO, 0, len(out))
	for _, d := range out {
		dtos = append(dtos, toDiscrepancyDTO(d))
	}
	return c.JSON(http.StatusOK, listDiscrepanciesResponse{Discrepancies: dtos})
}

// PATCH /discrepancies/{discrepancyId} — resolve / dismiss / reopen a finding.
func (h *DiscrepanciesController) PatchDiscrepancy(c echo.Context) error {
	auth := authFrom(c)
	userID, err := uuid.Parse(auth.UserID)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid auth context"})
	}
	id, err := uuid.Parse(c.Param("discrepancyId"))
	if err != nil {
		return c.JSON(http.StatusNotFound, errorResponse{Error: "discrepancy not found"})
	}

	var req patchDiscrepancyRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid request body"})
	}
	status := entity.DiscrepancyStatus(req.Status)
	if !entity.ValidDiscrepancyStatus(status) {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid status"})
	}

	d, err := h.Update.Execute(c.Request().Context(), userID, id, status)
	if err != nil {
		if errors.Is(err, orders.ErrDiscrepancyNotFound) {
			return c.JSON(http.StatusNotFound, errorResponse{Error: "discrepancy not found"})
		}
		h.Log.Error("update discrepancy failed", "error", err)
		return c.JSON(http.StatusInternalServerError, errorResponse{Error: "failed to update discrepancy"})
	}
	return c.JSON(http.StatusOK, toDiscrepancyDTO(d))
}
