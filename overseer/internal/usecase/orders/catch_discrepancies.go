package orders

import (
	"context"
	"fmt"

	"github.com/thirdeye/overseer/internal/entity"
)

// CatchDiscrepancies is the Audit usecase (spec: usecase/orders/CatchDiscrepancies).
// It rides the webhook job: when a known order conversation gets a new
// message, re-fetch the thread from Graph, ask the LLM to compare the new
// message against the history, store every finding, and push
// discrepancy.found for each. A clean thread stores nothing and publishes
// nothing — the DB stays the source of truth for clients that connect later.
type CatchDiscrepancies struct {
	Graph  ThreadFetcher
	LLM    DiscrepancyLLM
	Repo   DiscrepancyRepo
	Events EventPublisher
}

// Execute audits one new message in an order conversation. The order row
// provides ownership and business identity (account/user/company/PO); the
// new message provides the deeplink. The first message of a conversation has
// no history to contradict, so it never audits.
func (uc *CatchDiscrepancies) Execute(ctx context.Context, accessToken string, order entity.Order, newMsg entity.EmailMessage) ([]entity.Discrepancy, error) {
	msgs, err := uc.Graph.ListConversationMessages(ctx, accessToken, order.ConversationID)
	if err != nil {
		return nil, fmt.Errorf("fetch conversation %s: %w", order.ConversationID, err)
	}

	history := make([]entity.EmailMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.ID != newMsg.ID {
			history = append(history, m)
		}
	}
	if len(history) == 0 {
		return nil, nil // nothing previously agreed — nothing to contradict
	}

	findings, err := uc.LLM.FindDiscrepancies(ctx, newMsg, history)
	if err != nil {
		return nil, fmt.Errorf("find discrepancies (conversation %s): %w", order.ConversationID, err)
	}

	stored := make([]entity.Discrepancy, 0, len(findings))
	for _, f := range findings {
		d, err := uc.Repo.Insert(ctx, entity.Discrepancy{
			AccountID:        order.AccountID,
			UserID:           order.UserID,
			ConversationID:   order.ConversationID,
			Status:           entity.DiscrepancyOpen,
			CompanyName:      order.CompanyName,
			EmailAddress:     order.EmailAddress,
			EmailDeeplinkURL: newMsg.WebLink,
			OrderID:          order.OrderID,
			Priority:         f.Priority,
			Title:            f.Title,
			Detail:           f.Detail,
			Quotes:           f.Quotes,
		})
		if err != nil {
			return stored, fmt.Errorf("insert discrepancy: %w", err)
		}
		uc.Events.Publish(entity.Event{Type: entity.EventDiscrepancyFound, Data: d})
		stored = append(stored, d)
	}
	return stored, nil
}
