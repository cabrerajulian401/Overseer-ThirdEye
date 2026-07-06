// Package llm wraps the LLM provider (Anthropic) behind task-shaped methods.
// Prompts, model choice, retries, structured-output validation, and token/cost
// logging all live here — swapping models is a one-file change, and usecases
// only ever see clean structs or an error. Each method is exactly one model
// call per attempt; multi-step LLM flows are usecase-owned loops.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/thirdeye/overseer/internal/entity"
)

const DefaultModel = "claude-opus-4-8"

// validationRetries re-asks the model when its output fails schema/enum/date
// validation. Transport-level retries (429/5xx) are the SDK's job.
const validationRetries = 1

const (
	maxFilterPreviewChars = 500
	maxBodyChars          = 6000
	maxThreadMessages     = 20
)

type Gateway struct {
	client anthropic.Client
	model  anthropic.Model
	log    *slog.Logger
}

// New builds a Gateway. The Anthropic client reads ANTHROPIC_API_KEY from the
// environment. An empty model falls back to DefaultModel.
func New(model string, log *slog.Logger) *Gateway {
	if model == "" {
		model = DefaultModel
	}
	if log == nil {
		log = slog.Default()
	}
	return &Gateway{client: anthropic.NewClient(), model: anthropic.Model(model), log: log}
}

// --- FilterOrderMessages ----------------------------------------------------

const filterSystemPrompt = `You classify email conversations from a wholesale/reseller distributor's inbox for a procurement assistant.
Input: a JSON list of email messages; each message carries its conversationId, and a conversation may span several messages.
For EVERY distinct conversationId in the input, output exactly one decision object.
isGoodsOrder is true ONLY if the conversation is about the operator's company buying physical goods from a supplier: quotes, order confirmations, shipping/delivery updates, invoices for goods, ETA changes, or problems with a specific goods order.
Everything else is false — including newsletters and marketing, internal chatter, personal mail, and ALL account, platform, or software-service notifications (security alerts, sign-in warnings, app-connection or permission notices, welcome/onboarding emails, feature announcements). A message about an email account, an app, or an online service is never a goods order, even when it mentions "issues", "access", "connected", or "confirmation".
In each decision, state the explanation first, then the verdict. When in doubt, use false.`

// filterSchema demands one verdict per conversation rather than a bare list of
// keepers: the model must classify everything explicitly, and validation can
// reject incomplete answers. "explanation" sorts alphabetically before
// "isGoodsOrder" on purpose — Go marshals map keys sorted, and the model fills
// schema fields in order, so it reasons before it decides.
var filterSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"decisions": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"conversationId": map[string]any{"type": "string"},
					"explanation":    map[string]any{"type": "string"},
					"isGoodsOrder":   map[string]any{"type": "boolean"},
				},
				"required":             []string{"conversationId", "explanation", "isGoodsOrder"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"decisions"},
	"additionalProperties": false,
}

type filterDecision struct {
	ConversationID string `json:"conversationId"`
	Explanation    string `json:"explanation"`
	IsGoodsOrder   bool   `json:"isGoodsOrder"`
}

type filterResult struct {
	Decisions []filterDecision `json:"decisions"`
}

func (g *Gateway) FilterOrderMessages(ctx context.Context, msgs []entity.EmailMessage) ([]string, error) {
	if len(msgs) == 0 {
		return nil, nil
	}

	type filterMsg struct {
		ConversationID string `json:"conversationId"`
		From           string `json:"from"`
		FromName       string `json:"fromName"`
		Subject        string `json:"subject"`
		Preview        string `json:"preview"`
	}
	payload := make([]filterMsg, 0, len(msgs))
	known := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		known[m.ConversationID] = true
		payload = append(payload, filterMsg{
			ConversationID: m.ConversationID,
			From:           m.From,
			FromName:       m.FromName,
			Subject:        m.Subject,
			Preview:        truncate(m.BodyPreview, maxFilterPreviewChars),
		})
	}
	userMsg, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("llm: marshal filter payload: %w", err)
	}

	var kept []string
	err = g.callJSON(ctx, "FilterOrderMessages", filterSystemPrompt, string(userMsg), filterSchema, func(raw []byte) error {
		var out filterResult
		if err := json.Unmarshal(raw, &out); err != nil {
			return fmt.Errorf("invalid JSON: %w", err)
		}
		decided := make(map[string]bool, len(out.Decisions))
		for _, d := range out.Decisions {
			if !known[d.ConversationID] {
				return fmt.Errorf("unknown conversationId %q in model output", d.ConversationID)
			}
			decided[d.ConversationID] = decided[d.ConversationID] || d.IsGoodsOrder
		}
		for id := range known {
			if _, ok := decided[id]; !ok {
				return fmt.Errorf("no decision for conversationId %q", id)
			}
		}
		kept = kept[:0]
		for _, d := range out.Decisions {
			if decided[d.ConversationID] {
				kept = append(kept, d.ConversationID)
				decided[d.ConversationID] = false // dedupe: emit each kept id once
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return kept, nil
}

// --- ExtractOrderFields -----------------------------------------------------

const extractSystemPrompt = `You extract structured purchase-order data from a supplier email thread for a wholesale distributor's order list.
Read the whole thread (oldest to newest) and report the CURRENT state of the order.
Field guidance:
- companyName: the supplier company. Infer from signature blocks or the sender's domain if not stated.
- emailAddress: the supplier contact's email address.
- orderId: the supplier's purchase-order number as an integer; 0 if none is mentioned.
- status: "confirmed" (order placed/acknowledged), "in_transit" (shipped/on the way), "delivered" (received), or "issue" (problem, delay dispute, discrepancy).
- originalEstDate: the FIRST promised delivery/ship date in the thread (YYYY-MM-DD), or null.
- currentEstDate: the LATEST promised delivery/ship date (YYYY-MM-DD), or null.
- lastMessenger: display name of whoever sent the newest message.
- supportingQuote: a short verbatim quote from the thread that best evidences the status.`

var extractSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"companyName":  map[string]any{"type": "string"},
		"emailAddress": map[string]any{"type": "string"},
		"orderId":      map[string]any{"type": "integer"},
		"status": map[string]any{
			"type": "string",
			"enum": []string{"in_transit", "confirmed", "delivered", "issue"},
		},
		"originalEstDate": map[string]any{"type": []string{"string", "null"}},
		"currentEstDate":  map[string]any{"type": []string{"string", "null"}},
		"lastMessenger":   map[string]any{"type": "string"},
		"supportingQuote": map[string]any{"type": "string"},
	},
	"required": []string{
		"companyName", "emailAddress", "orderId", "status",
		"originalEstDate", "currentEstDate", "lastMessenger", "supportingQuote",
	},
	"additionalProperties": false,
}

type extractResult struct {
	CompanyName     string  `json:"companyName"`
	EmailAddress    string  `json:"emailAddress"`
	OrderID         int     `json:"orderId"`
	Status          string  `json:"status"`
	OriginalEstDate *string `json:"originalEstDate"`
	CurrentEstDate  *string `json:"currentEstDate"`
	LastMessenger   string  `json:"lastMessenger"`
	SupportingQuote string  `json:"supportingQuote"`
}

func (g *Gateway) ExtractOrderFields(ctx context.Context, thread entity.Thread) (entity.OrderExtraction, error) {
	type threadMsg struct {
		From     string `json:"from"`
		FromName string `json:"fromName"`
		Date     string `json:"date"`
		Subject  string `json:"subject"`
		Body     string `json:"body"`
	}
	msgs := thread.Messages
	if len(msgs) > maxThreadMessages {
		msgs = msgs[len(msgs)-maxThreadMessages:]
	}
	payload := make([]threadMsg, 0, len(msgs))
	for _, m := range msgs {
		body := m.Body
		if body == "" {
			body = m.BodyPreview
		}
		payload = append(payload, threadMsg{
			From:     m.From,
			FromName: m.FromName,
			Date:     m.CreatedDateTime.Format(time.RFC3339),
			Subject:  m.Subject,
			Body:     truncate(body, maxBodyChars),
		})
	}
	userMsg, err := json.Marshal(payload)
	if err != nil {
		return entity.OrderExtraction{}, fmt.Errorf("llm: marshal thread payload: %w", err)
	}

	var extraction entity.OrderExtraction
	err = g.callJSON(ctx, "ExtractOrderFields", extractSystemPrompt, string(userMsg), extractSchema, func(raw []byte) error {
		var out extractResult
		if err := json.Unmarshal(raw, &out); err != nil {
			return fmt.Errorf("invalid JSON: %w", err)
		}
		x, err := toExtraction(out)
		if err != nil {
			return err
		}
		extraction = x
		return nil
	})
	if err != nil {
		return entity.OrderExtraction{}, err
	}
	return extraction, nil
}

// toExtraction validates model output at the gateway boundary: dates must
// parse, the status must match the enum shared with the DB CHECK vocabulary.
func toExtraction(out extractResult) (entity.OrderExtraction, error) {
	status := entity.OrderStatus(out.Status)
	if !entity.ValidOrderStatus(status) {
		return entity.OrderExtraction{}, fmt.Errorf("invalid status %q", out.Status)
	}
	original, err := parseDate(out.OriginalEstDate)
	if err != nil {
		return entity.OrderExtraction{}, fmt.Errorf("originalEstDate: %w", err)
	}
	current, err := parseDate(out.CurrentEstDate)
	if err != nil {
		return entity.OrderExtraction{}, fmt.Errorf("currentEstDate: %w", err)
	}
	return entity.OrderExtraction{
		CompanyName:     out.CompanyName,
		EmailAddress:    out.EmailAddress,
		OrderID:         out.OrderID,
		Status:          status,
		OriginalEstDate: original,
		CurrentEstDate:  current,
		LastMessenger:   out.LastMessenger,
		SupportingQuote: out.SupportingQuote,
	}, nil
}

// --- shared plumbing ----------------------------------------------------------

// callJSON makes one structured-output model call per attempt, validates the
// JSON with the supplied validate func, and retries validation failures.
func (g *Gateway) callJSON(ctx context.Context, task, system, user string, schema map[string]any, validate func(raw []byte) error) error {
	var lastErr error
	for attempt := 0; attempt <= validationRetries; attempt++ {
		raw, err := g.complete(ctx, task, system, user, schema)
		if err != nil {
			return err
		}
		if err := validate(raw); err != nil {
			lastErr = err
			g.log.Warn("llm output validation failed",
				"task", task, "attempt", attempt+1, "error", err, "raw", string(raw))
			continue
		}
		return nil
	}
	return fmt.Errorf("llm: %s: output validation failed after %d attempts: %w", task, validationRetries+1, lastErr)
}

func (g *Gateway) complete(ctx context.Context, task, system, user string, schema map[string]any) ([]byte, error) {
	started := time.Now()
	resp, err := g.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     g.model,
		MaxTokens: 16000,
		System: []anthropic.TextBlockParam{
			{Text: system},
		},
		Thinking: anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}},
		OutputConfig: anthropic.OutputConfigParam{
			Format: anthropic.JSONOutputFormatParam{Schema: schema},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(user)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("llm: %s: %w", task, err)
	}

	// Cost logging from day one: the initial sync alone pushes a month of
	// messages through this gateway.
	g.log.Info("llm call",
		"task", task,
		"model", resp.Model,
		"input_tokens", resp.Usage.InputTokens,
		"output_tokens", resp.Usage.OutputTokens,
		"duration_ms", time.Since(started).Milliseconds(),
	)

	if resp.StopReason == anthropic.StopReasonRefusal {
		return nil, fmt.Errorf("llm: %s: model refused the request", task)
	}
	for _, block := range resp.Content {
		if text, ok := block.AsAny().(anthropic.TextBlock); ok {
			g.log.Debug("llm raw response", "task", task, "raw", text.Text)
			return []byte(text.Text), nil
		}
	}
	return nil, fmt.Errorf("llm: %s: no text block in response (stop_reason=%s)", task, resp.StopReason)
}

func parseDate(s *string) (*time.Time, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", *s)
	if err != nil {
		return nil, fmt.Errorf("expected YYYY-MM-DD, got %q", *s)
	}
	return &t, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…[truncated]"
}
