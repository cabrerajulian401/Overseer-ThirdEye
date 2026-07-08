package messages

import (
	"context"
	"fmt"
	"slices"

	"github.com/thirdeye/overseer/internal/entity"
)

// HandleChangeMessage is the webhook job (spec: jobs/HandleChangeMessage):
// notification → resolve subscription to user → fetch the message → LLM
// structure → merge-upsert into orders → order.updated on the realtime
// channel → Audit (CatchDiscrepancies). Graph delivers at-least-once, so the
// whole path is idempotent — the upsert is keyed on (user_id, conversation_id).
//
// Extension point (Checkups, not built yet): this is the only place that sees
// supplier replies — when checkup_messages exists, cancel pending rows with
// cancel_if_reply on this conversation here.
type HandleChangeMessage struct {
	Subs   SubscriptionResolver
	Tokens TokenSource
	Latest *GetLatestMessage
	LLM    OrderLLM
	Repo   OrderRepo
	Events EventPublisher

	// Audit is the Discrepancy leg (spec: Audit rides this job and has no
	// infrastructure of its own). nil disables it.
	Audit DiscrepancyCatcher
}

type HandleInput struct {
	SubscriptionID string // Graph's subscription id from the notification
	MessageID      string // Graph message id from resourceData
}

// HandleOutcome tells the caller (for logging) what the job did; skips are
// normal operation, not errors.
type HandleOutcome string

const (
	OutcomeCreated                    HandleOutcome = "created"
	OutcomeUpdated                    HandleOutcome = "updated"
	OutcomeSkippedNotOrder            HandleOutcome = "skipped_not_order"
	OutcomeSkippedUnknownSubscription HandleOutcome = "skipped_unknown_subscription"
)

func (uc *HandleChangeMessage) Execute(ctx context.Context, in HandleInput) (HandleOutcome, error) {
	user, ok, err := uc.Subs.ResolveSubscription(ctx, in.SubscriptionID)
	if err != nil {
		return "", fmt.Errorf("resolve subscription: %w", err)
	}
	if !ok {
		return OutcomeSkippedUnknownSubscription, nil
	}

	token, ok := uc.Tokens.GraphToken(user.ID)
	if !ok {
		return "", fmt.Errorf("no cached graph token for user %s", user.ID)
	}

	msg, err := uc.Latest.Execute(ctx, token, in.MessageID)
	if err != nil {
		return "", err
	}
	thread := entity.Thread{ConversationID: msg.ConversationID, Messages: []entity.EmailMessage{msg}}

	existing, found, err := uc.Repo.GetByConversation(ctx, user.ID, msg.ConversationID)
	if err != nil {
		return "", fmt.Errorf("get order by conversation: %w", err)
	}

	// Unseen conversations get classified first — without this gate every
	// newsletter that arrives post-sync would become an order row. Known
	// order conversations were classified at sync time; skip straight to
	// extraction.
	if !found {
		kept, err := uc.LLM.FilterOrderMessages(ctx, thread.Messages)
		if err != nil {
			return "", fmt.Errorf("filter message: %w", err)
		}
		if !slices.Contains(kept, msg.ConversationID) {
			return OutcomeSkippedNotOrder, nil
		}
	}

	extraction, err := uc.LLM.ExtractOrderFields(ctx, thread)
	if err != nil {
		return "", fmt.Errorf("extract order fields (conversation %s): %w", msg.ConversationID, err)
	}

	outcome := OutcomeCreated
	var order entity.Order
	if found {
		outcome = OutcomeUpdated
		order = mergeExtraction(existing, extraction, msg)
	} else {
		order = entity.OrderFromExtraction(thread, extraction)
		order.AccountID = user.AccountID
		order.UserID = user.ID
	}

	if err := uc.Repo.Upsert(ctx, order); err != nil {
		return "", fmt.Errorf("upsert order (conversation %s): %w", msg.ConversationID, err)
	}

	// Re-read so the event carries the authoritative row (id, timestamps).
	if saved, ok, err := uc.Repo.GetByConversation(ctx, user.ID, msg.ConversationID); err == nil && ok {
		order = saved
	}

	uc.Events.Publish(entity.Event{Type: entity.EventOrderUpdated, Data: order})

	// Audit rides this job. The order work above is already committed — an
	// audit failure surfaces to the caller's log but cannot undo the upsert,
	// and a Graph re-delivery would retry it.
	if uc.Audit != nil {
		if _, err := uc.Audit.Execute(ctx, token, order, msg); err != nil {
			return outcome, fmt.Errorf("audit (conversation %s): %w", msg.ConversationID, err)
		}
	}
	return outcome, nil
}

// mergeExtraction folds a single new message's extraction into the existing
// order. One message rarely mentions every field, so empty extraction fields
// keep their stored values; OriginalEstDate never moves once set.
func mergeExtraction(existing entity.Order, x entity.OrderExtraction, latest entity.EmailMessage) entity.Order {
	o := existing
	if o.CompanyName == "" {
		o.CompanyName = x.CompanyName
	}
	if x.OrderID != 0 {
		o.OrderID = x.OrderID
	}
	if entity.ValidOrderStatus(x.Status) {
		o.Status = x.Status
	}
	if o.OriginalEstDate == nil {
		o.OriginalEstDate = x.OriginalEstDate
	}
	if x.CurrentEstDate != nil {
		o.CurrentEstDate = x.CurrentEstDate
	}
	if x.SupportingQuote != "" {
		o.SupportingQuote = x.SupportingQuote
	}
	switch {
	case x.LastMessenger != "":
		o.LastMessenger = x.LastMessenger
	case latest.FromName != "":
		o.LastMessenger = latest.FromName
	}
	if latest.WebLink != "" {
		o.EmailURL = latest.WebLink
	}
	if o.EmailAddress == "" {
		o.EmailAddress = latest.From
	}
	return o
}
