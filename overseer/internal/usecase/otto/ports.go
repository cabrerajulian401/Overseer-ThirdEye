// Package otto holds the Otto usecases: resolve destinations for a scanned
// email, and record the user-approved order into the chosen sheet. Ports
// live where they are consumed.
package otto

import (
	"context"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// ThreadFetcher is the slice of gateway/graph Otto needs — the full
// conversation, reused from the audit's read path (spec: reuse Graph calls,
// don't duplicate per feature).
type ThreadFetcher interface {
	ListConversationMessages(ctx context.Context, accessToken, conversationID string) ([]entity.EmailMessage, error)
}

// DriveSearcher is the slice of gateway/drive the scan loop needs.
type DriveSearcher interface {
	Search(ctx context.Context, accessToken, query string) ([]entity.DriveItem, error)
	Recent(ctx context.Context, accessToken string) ([]entity.DriveItem, error)
}

// ItemGetter fetches one drive item — RecordOrder's webUrl lookup.
type ItemGetter interface {
	GetItem(ctx context.Context, accessToken, itemID string) (entity.DriveItem, error)
}

// OttoLLM is the slice of gateway/llm the scan loop needs.
type OttoLLM interface {
	ExtractOrderFields(ctx context.Context, thread entity.Thread) (entity.OrderExtraction, error)
	RankDestinations(ctx context.Context, fields entity.OrderExtraction, candidates []entity.DriveItem) ([]entity.DriveItem, error)
}

// PlanLLM is the slice of gateway/llm RecordOrder needs.
type PlanLLM interface {
	PlanSheetAppend(ctx context.Context, structure entity.WorkbookStructure, fields entity.OrderExtraction) (entity.SheetAppendPlan, error)
}

// Workbook is the slice of gateway/excel RecordOrder needs.
type Workbook interface {
	GetWorkbookStructure(ctx context.Context, accessToken, itemID string) (entity.WorkbookStructure, error)
	AppendTableRow(ctx context.Context, accessToken, itemID, tableName string, values []string) error
}

// ScanRepo is the otto_scans-table port. Every query filters by owner.
type ScanRepo interface {
	// Insert stores a scan and returns the stored row (server-minted id,
	// timestamps).
	Insert(ctx context.Context, s entity.OttoScan) (entity.OttoScan, error)
	// GetByID returns the user's scan; found=false when no such row belongs
	// to the user.
	GetByID(ctx context.Context, userID, id uuid.UUID) (entity.OttoScan, bool, error)
	// MarkRecorded sets status='recorded' and the chosen resource.
	MarkRecorded(ctx context.Context, userID, id uuid.UUID, selectedResource string) error
}
