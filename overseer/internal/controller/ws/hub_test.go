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
