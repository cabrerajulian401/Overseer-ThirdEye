package orders

import (
	"context"
	"fmt"
	"sort"

	"github.com/thirdeye/overseer/internal/entity"
)

// FormatInitialOrderData turns a raw message payload into order entities:
// llm.FilterOrderMessages drops non-order threads, then llm.ExtractOrderFields
// structures each kept thread. The returned orders carry conversation-level
// fields only — the caller stamps ownership (account/user) and persists.
//
// Any LLM error aborts the whole batch: the caller must never see a
// half-mapped result, so it either gets every order or an error.
type FormatInitialOrderData struct {
	LLM OrderLLM
}

func (uc *FormatInitialOrderData) Execute(ctx context.Context, msgs []entity.EmailMessage) ([]entity.Order, error) {
	if len(msgs) == 0 {
		return nil, nil
	}

	kept, err := uc.LLM.FilterOrderMessages(ctx, msgs)
	if err != nil {
		return nil, fmt.Errorf("filter order messages: %w", err)
	}
	if len(kept) == 0 {
		return nil, nil
	}

	threads := groupThreads(msgs, kept)

	orders := make([]entity.Order, 0, len(threads))
	for _, thread := range threads {
		extraction, err := uc.LLM.ExtractOrderFields(ctx, thread)
		if err != nil {
			return nil, fmt.Errorf("extract order fields (conversation %s): %w", thread.ConversationID, err)
		}
		orders = append(orders, orderFromExtraction(thread, extraction))
	}
	return orders, nil
}

// groupThreads assembles the kept conversations' messages into threads,
// oldest → newest, in a deterministic conversation order.
func groupThreads(msgs []entity.EmailMessage, keptConversationIDs []string) []entity.Thread {
	keep := make(map[string]bool, len(keptConversationIDs))
	for _, id := range keptConversationIDs {
		keep[id] = true
	}

	byConversation := make(map[string][]entity.EmailMessage)
	for _, m := range msgs {
		if keep[m.ConversationID] {
			byConversation[m.ConversationID] = append(byConversation[m.ConversationID], m)
		}
	}

	ids := make([]string, 0, len(byConversation))
	for id := range byConversation {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	threads := make([]entity.Thread, 0, len(ids))
	for _, id := range ids {
		ms := byConversation[id]
		sort.Slice(ms, func(i, j int) bool { return ms[i].CreatedDateTime.Before(ms[j].CreatedDateTime) })
		threads = append(threads, entity.Thread{ConversationID: id, Messages: ms})
	}
	return threads
}

func orderFromExtraction(thread entity.Thread, x entity.OrderExtraction) entity.Order {
	latest := thread.LatestMessage()
	o := entity.Order{
		ConversationID:  thread.ConversationID,
		CompanyName:     x.CompanyName,
		EmailAddress:    x.EmailAddress,
		OrderID:         x.OrderID,
		EmailURL:        latest.WebLink,
		Status:          x.Status,
		OriginalEstDate: x.OriginalEstDate,
		CurrentEstDate:  x.CurrentEstDate,
		LastMessenger:   x.LastMessenger,
		SupportingQuote: x.SupportingQuote,
	}
	if o.LastMessenger == "" {
		o.LastMessenger = latest.FromName
	}
	if o.EmailAddress == "" {
		o.EmailAddress = latest.From
	}
	return o
}
