package otto

import (
	"context"
	"fmt"
	"strconv"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// ResolveDestinations is the scan agent loop (spec: usecase/otto/ResolveDestinations,
// renamed from findExcelResources — named for functionality, not the current
// integration). Three plain gateway calls in sequence — extract → drive
// search → rank — orchestrated here where every step is loggable; each LLM
// method is exactly one model call.
type ResolveDestinations struct {
	Graph ThreadFetcher
	Drive DriveSearcher
	LLM   OttoLLM
	Repo  ScanRepo
}

type ResolveInput struct {
	AccountID uuid.UUID
	UserID    uuid.UUID
	// GraphToken authenticates both the mail read and the drive searches.
	GraphToken     string
	MessageID      string
	ConversationID string
}

type ResolveOutput struct {
	Scan         entity.OttoScan
	Destinations []entity.DriveItem // top 3, best first — the confirm card renders these
}

// Execute scans one email: extract fields from the whole thread, find
// candidate sheets, rank the top 3. The scan row is stored only when the
// whole loop succeeds — a failed scan leaves no row behind — and is stored
// before returning, so the record call can always find it.
func (uc *ResolveDestinations) Execute(ctx context.Context, in ResolveInput) (ResolveOutput, error) {
	msgs, err := uc.Graph.ListConversationMessages(ctx, in.GraphToken, in.ConversationID)
	if err != nil {
		return ResolveOutput{}, fmt.Errorf("fetch conversation %s: %w", in.ConversationID, err)
	}
	if len(msgs) == 0 {
		return ResolveOutput{}, fmt.Errorf("conversation %s has no messages", in.ConversationID)
	}
	thread := entity.Thread{ConversationID: in.ConversationID, Messages: msgs}

	fields, err := uc.LLM.ExtractOrderFields(ctx, thread)
	if err != nil {
		return ResolveOutput{}, fmt.Errorf("extract order fields (conversation %s): %w", in.ConversationID, err)
	}

	candidates, err := uc.findCandidates(ctx, in.GraphToken, fields)
	if err != nil {
		return ResolveOutput{}, err
	}

	var destinations []entity.DriveItem
	if len(candidates) > 0 {
		destinations, err = uc.LLM.RankDestinations(ctx, fields, candidates)
		if err != nil {
			return ResolveOutput{}, fmt.Errorf("rank destinations: %w", err)
		}
	}

	scan, err := uc.Repo.Insert(ctx, entity.OttoScan{
		AccountID:      in.AccountID,
		UserID:         in.UserID,
		MessageID:      in.MessageID,
		ConversationID: in.ConversationID,
		EmailURL:       thread.LatestMessage().WebLink,
		Fields:         fields,
		Status:         entity.ScanScanned,
	})
	if err != nil {
		return ResolveOutput{}, fmt.Errorf("insert scan: %w", err)
	}
	return ResolveOutput{Scan: scan, Destinations: destinations}, nil
}

// findCandidates runs the spec's search queries — supplier name, PO number,
// generic log names, plus /recent for cold starts — and dedupes by item id,
// preserving first-seen order.
func (uc *ResolveDestinations) findCandidates(ctx context.Context, token string, fields entity.OrderExtraction) ([]entity.DriveItem, error) {
	queries := make([]string, 0, 4)
	if fields.CompanyName != "" {
		queries = append(queries, fields.CompanyName)
	}
	if fields.OrderID != 0 {
		queries = append(queries, strconv.Itoa(fields.OrderID))
	}
	queries = append(queries, "orders", "PO")

	seen := make(map[string]bool)
	var candidates []entity.DriveItem
	add := func(items []entity.DriveItem) {
		for _, it := range items {
			if !seen[it.ID] {
				seen[it.ID] = true
				candidates = append(candidates, it)
			}
		}
	}

	for _, q := range queries {
		items, err := uc.Drive.Search(ctx, token, q)
		if err != nil {
			return nil, fmt.Errorf("drive search %q: %w", q, err)
		}
		add(items)
	}
	recent, err := uc.Drive.Recent(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("drive recent: %w", err)
	}
	add(recent)
	return candidates, nil
}
