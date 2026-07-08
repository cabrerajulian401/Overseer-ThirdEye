package http

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/usecase/otto"
)

// OttoController is transport only: parse → call usecase → map to DTO.
type OttoController struct {
	Resolve *otto.ResolveDestinations
	Record  *otto.RecordOrder

	Log interface {
		Error(msg string, args ...any)
	}
}

// POST /otto/scans — "Ask Otto": extract fields + top-3 destinations. Fans
// out to Graph + LLM synchronously; the spec's 202-and-push escape hatch
// stays available if it runs long in practice.
func (h *OttoController) PostScans(c echo.Context) error {
	auth := authFrom(c)
	accountID, errA := uuid.Parse(auth.AccountID)
	userID, errU := uuid.Parse(auth.UserID)
	if errA != nil || errU != nil {
		return c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid auth context"})
	}

	var req scanRequest
	if err := c.Bind(&req); err != nil || req.MessageID == "" || req.ConversationID == "" {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "messageId and conversationId are required"})
	}

	out, err := h.Resolve.Execute(c.Request().Context(), otto.ResolveInput{
		AccountID:      accountID,
		UserID:         userID,
		GraphToken:     auth.GraphToken,
		MessageID:      req.MessageID,
		ConversationID: req.ConversationID,
	})
	if err != nil {
		h.Log.Error("otto scan failed", "conversationId", req.ConversationID, "error", err)
		return c.JSON(http.StatusBadGateway, errorResponse{Error: "scan failed"})
	}

	dests := make([]destinationDTO, 0, len(out.Destinations))
	for _, d := range out.Destinations {
		dests = append(dests, destinationDTO{ID: d.ID, Name: d.Name, WebURL: d.WebURL})
	}
	return c.JSON(http.StatusOK, scanResponse{
		ScanID:          out.Scan.ID.String(),
		ExtractedFields: toExtractedFieldsDTO(out.Scan.Fields),
		Destinations:    dests,
	})
}

// POST /otto/scans/{scanId}/record — the human-in-the-loop approval: nothing
// is ever written to the ERP until this call.
func (h *OttoController) PostRecord(c echo.Context) error {
	auth := authFrom(c)
	userID, err := uuid.Parse(auth.UserID)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid auth context"})
	}
	scanID, err := uuid.Parse(c.Param("scanId"))
	if err != nil {
		return c.JSON(http.StatusNotFound, errorResponse{Error: "scan not found"})
	}

	var req recordRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid request body"})
	}
	if req.SelectedResource == "" {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "selectedResource is required"})
	}
	fields, err := fromExtractedFieldsDTO(req.Fields)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid fields"})
	}

	webURL, err := h.Record.Execute(c.Request().Context(), otto.RecordInput{
		UserID:           userID,
		GraphToken:       auth.GraphToken,
		ScanID:           scanID,
		SelectedResource: req.SelectedResource,
		Fields:           fields,
	})
	if err != nil {
		switch {
		case errors.Is(err, otto.ErrScanNotFound):
			return c.JSON(http.StatusNotFound, errorResponse{Error: "scan not found"})
		case errors.Is(err, otto.ErrScanAlreadyRecorded):
			return c.JSON(http.StatusConflict, errorResponse{Error: "scan already recorded"})
		default:
			h.Log.Error("otto record failed", "scanId", scanID, "error", err)
			return c.JSON(http.StatusBadGateway, errorResponse{Error: "record failed"})
		}
	}
	return c.JSON(http.StatusOK, recordResponse{WebURL: webURL, Status: string(entity.ScanRecorded)})
}
