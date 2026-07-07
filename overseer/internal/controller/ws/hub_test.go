package ws

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

func TestToDTOOrder(t *testing.T) {
	est := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	o := entity.Order{
		ID: uuid.New(), ConversationID: "conv-1",
		CompanyName: "Delmar Tooling", EmailAddress: "dana@delmar.com",
		OrderID: 4471, EmailURL: "https://outlook.example/msg-2",
		Status: entity.OrderStatusInTransit, CurrentEstDate: &est,
		LastMessenger: "Dana Delmar", SupportingQuote: "Pushed to July 18",
		CreatedAt: time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC),
	}

	raw, err := json.Marshal(toDTO(o))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	if got["companyName"] != "Delmar Tooling" || got["orderId"] != float64(4471) ||
		got["status"] != "in_transit" || got["conversationId"] != "conv-1" {
		t.Errorf("dto = %v", got)
	}
	if got["currentEstDate"] != "2026-07-18" {
		t.Errorf("currentEstDate = %v", got["currentEstDate"])
	}
	if got["originalEstDate"] != nil {
		t.Errorf("originalEstDate = %v, want null", got["originalEstDate"])
	}
	if got["dateCreated"] != "2026-07-05T10:00:00Z" {
		t.Errorf("dateCreated = %v", got["dateCreated"])
	}
}

func TestToDTODiscrepancy(t *testing.T) {
	d := entity.Discrepancy{
		ID: uuid.New(), ConversationID: "conv-1",
		Status: entity.DiscrepancyOpen, CompanyName: "Delmar Tooling",
		EmailAddress: "dana@delmar.com", EmailDeeplinkURL: "https://outlook.example/msg-9",
		OrderID: 4471, Priority: entity.PriorityHigh,
		Title: "Price raised $41.00 → $42.50/unit",
		Detail: entity.DiscrepancyDetail{
			PreviouslyAgreed: "$41.00/unit", NewMetric: "$42.50/unit", Type: entity.MetricPrice,
		},
		Quotes:    []entity.DiscrepancyQuote{{Quote: "we agree on $41.00/unit", MessageLink: "https://outlook.example/m-2"}},
		CreatedAt: time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
	}

	raw, err := json.Marshal(toDTO(d))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	if got["companyName"] != "Delmar Tooling" || got["status"] != "open" ||
		got["priority"] != "high" || got["orderId"] != float64(4471) ||
		got["title"] != "Price raised $41.00 → $42.50/unit" ||
		got["emailDeeplinkUrl"] != "https://outlook.example/msg-9" ||
		got["conversationId"] != "conv-1" {
		t.Errorf("dto = %v", got)
	}
	detail, ok := got["detail"].(map[string]any)
	if !ok || detail["previouslyAgreed"] != "$41.00/unit" ||
		detail["newMetric"] != "$42.50/unit" || detail["type"] != "price" {
		t.Errorf("detail = %v", got["detail"])
	}
	quotes, ok := got["quotes"].([]any)
	if !ok || len(quotes) != 1 {
		t.Fatalf("quotes = %v", got["quotes"])
	}
	q := quotes[0].(map[string]any)
	if q["quote"] != "we agree on $41.00/unit" || q["messageLink"] != "https://outlook.example/m-2" {
		t.Errorf("quote = %v", q)
	}
	if got["dateCreated"] != "2026-07-07T10:00:00Z" {
		t.Errorf("dateCreated = %v", got["dateCreated"])
	}
}
