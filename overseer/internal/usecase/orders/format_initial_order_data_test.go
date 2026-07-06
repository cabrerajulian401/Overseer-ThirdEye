package orders

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thirdeye/overseer/internal/entity"
)

func TestFormatInitialOrderData_EmptyInputSkipsLLM(t *testing.T) {
	llm := &stubLLM{}
	uc := &FormatInitialOrderData{LLM: llm}

	out, err := uc.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != nil {
		t.Fatalf("expected no orders, got %v", out)
	}
}

func TestFormatInitialOrderData_FilterErrorSurfaces(t *testing.T) {
	llm := &stubLLM{filterErr: errors.New("bad model output")}
	uc := &FormatInitialOrderData{LLM: llm}

	_, err := uc.Execute(context.Background(), []entity.EmailMessage{
		msg("conv-1", "a@x.com", "subject", time.Now()),
	})
	if err == nil {
		t.Fatal("malformed LLM output must surface as an error, never a half-mapped entity")
	}
}

func TestFormatInitialOrderData_ExtractErrorSurfaces(t *testing.T) {
	llm := &stubLLM{
		filterResult: []string{"conv-1"},
		extractErr:   errors.New("bad model output"),
	}
	uc := &FormatInitialOrderData{LLM: llm}

	out, err := uc.Execute(context.Background(), []entity.EmailMessage{
		msg("conv-1", "a@x.com", "subject", time.Now()),
	})
	if err == nil {
		t.Fatal("extract error must surface as an error")
	}
	if out != nil {
		t.Fatalf("no partial result allowed on error, got %v", out)
	}
}

func TestFormatInitialOrderData_ThreadAssemblyAndFallbacks(t *testing.T) {
	older := time.Now().Add(-2 * time.Hour)
	newer := time.Now()

	m1 := msg("conv-1", "maria@delmar.com", "Quote", older)
	m2 := msg("conv-1", "maria@delmar.com", "Re: Quote", newer)
	m2.WebLink = "https://outlook.example/msg2"
	m2.FromName = "Maria Delmar"

	llm := &stubLLM{
		filterResult: []string{"conv-1"},
		extractions: map[string]entity.OrderExtraction{
			// LastMessenger/EmailAddress intentionally blank → fall back to
			// the newest message's sender.
			"conv-1": {CompanyName: "Delmar Tooling", OrderID: 4471, Status: entity.OrderStatusInTransit},
		},
	}
	uc := &FormatInitialOrderData{LLM: llm}

	out, err := uc.Execute(context.Background(), []entity.EmailMessage{m2, m1}) // out of order on purpose
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected one order, got %d", len(out))
	}
	o := out[0]
	if o.EmailURL != "https://outlook.example/msg2" {
		t.Fatalf("emailUrl must come from the newest message, got %q", o.EmailURL)
	}
	if o.LastMessenger != "Maria Delmar" {
		t.Fatalf("lastMessenger fallback failed, got %q", o.LastMessenger)
	}
	if o.EmailAddress != "maria@delmar.com" {
		t.Fatalf("emailAddress fallback failed, got %q", o.EmailAddress)
	}
}
