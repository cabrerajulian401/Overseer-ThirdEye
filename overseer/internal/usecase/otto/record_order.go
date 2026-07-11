package otto

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// ErrScanNotFound reports a scanId that doesn't exist or belongs to another
// user — the controller maps it to 404.
var ErrScanNotFound = errors.New("scan not found")

// ErrScanAlreadyRecorded reports a second approval of the same scan — the
// controller maps it to 409. Nothing is written twice.
var ErrScanAlreadyRecorded = errors.New("scan already recorded")

// RecordOrder is the human-in-the-loop approval (spec: nothing is ever
// written to the ERP until this happens). The LLM plans where/how to append;
// the usecase writes; the values are the user-approved fields from the
// confirm card — never the stored raw extraction.
type RecordOrder struct {
	Repo  ScanRepo
	Excel Workbook
	LLM   PlanLLM
	Drive ItemGetter
}

type RecordInput struct {
	UserID     uuid.UUID
	GraphToken string
	ScanID     uuid.UUID
	// SelectedResource is the drive item id of the destination the user picked.
	SelectedResource string
	// Fields is what the user approved on the card — required to be the
	// edited version, otherwise the confirm step is theater.
	Fields entity.OrderExtraction
}

// Execute appends the approved order to the chosen workbook and returns its
// webUrl for the "Recorded ✓ — open sheet" card. The scan is marked
// recorded only after the write lands.
func (uc *RecordOrder) Execute(ctx context.Context, in RecordInput) (string, error) {
	scan, found, err := uc.Repo.GetByID(ctx, in.UserID, in.ScanID)
	if err != nil {
		return "", fmt.Errorf("get scan: %w", err)
	}
	if !found {
		return "", ErrScanNotFound
	}
	if scan.Status == entity.ScanRecorded {
		return "", ErrScanAlreadyRecorded
	}

	structure, err := uc.Excel.GetWorkbookStructure(ctx, in.GraphToken, in.SelectedResource)
	if err != nil {
		return "", fmt.Errorf("read workbook structure: %w", err)
	}

	plan, err := uc.LLM.PlanSheetAppend(ctx, structure, in.Fields)
	if err != nil {
		return "", fmt.Errorf("plan sheet append: %w", err)
	}

	if err := uc.Excel.AppendTableRow(ctx, in.GraphToken, in.SelectedResource, plan.Table, plan.Values); err != nil {
		return "", fmt.Errorf("append row: %w", err)
	}

	item, err := uc.Drive.GetItem(ctx, in.GraphToken, in.SelectedResource)
	if err != nil {
		return "", fmt.Errorf("get sheet url: %w", err)
	}

	if err := uc.Repo.MarkRecorded(ctx, in.UserID, in.ScanID, in.SelectedResource); err != nil {
		return "", fmt.Errorf("mark scan recorded: %w", err)
	}
	return item.WebURL, nil
}
