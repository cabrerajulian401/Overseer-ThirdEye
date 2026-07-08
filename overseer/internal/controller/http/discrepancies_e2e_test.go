package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

func (e *env) seedDiscrepancy(t *testing.T, status entity.DiscrepancyStatus, title string) entity.Discrepancy {
	t.Helper()
	d, err := e.discs.Insert(context.Background(), entity.Discrepancy{
		AccountID: e.user.AccountID, UserID: e.user.ID, ConversationID: "conv-1",
		Status: status, CompanyName: "Delmar Tooling", EmailAddress: "dana@delmar.com",
		EmailDeeplinkURL: "https://outlook.example/msg-9", OrderID: 4471,
		Priority: entity.PriorityHigh, Title: title,
		Detail: entity.DiscrepancyDetail{
			PreviouslyAgreed: "$41.00/unit", NewMetric: "$42.50/unit", Type: entity.MetricPrice,
		},
		Quotes: []entity.DiscrepancyQuote{{Quote: "we agree on $41.00/unit", MessageLink: "https://outlook.example/m-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (e *env) patchJSON(t *testing.T, path, body string, authed bool) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authed {
		req.Header.Set("Authorization", "Bearer graph-token-abc")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestGetDiscrepancies_E2E_Returns200CamelCase(t *testing.T) {
	e := newEnv(t)
	e.seedDiscrepancy(t, entity.DiscrepancyOpen, "Price raised $41.00 → $42.50/unit")

	resp, body := e.request(t, http.MethodGet, "/discrepancies", true)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	list, ok := body["discrepancies"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("body = %v", body)
	}
	first := list[0].(map[string]any)
	for _, key := range []string{"id", "companyName", "orderId", "status", "priority", "title", "detail", "quotes", "dateCreated", "conversationId", "emailDeeplinkUrl"} {
		if _, ok := first[key]; !ok {
			t.Errorf("missing camelCase key %q in %v", key, first)
		}
	}
	detail := first["detail"].(map[string]any)
	if detail["previouslyAgreed"] != "$41.00/unit" || detail["type"] != "price" {
		t.Errorf("detail = %v", detail)
	}
}

func TestGetDiscrepancies_E2E_StatusFilterAndAuth(t *testing.T) {
	e := newEnv(t)
	e.seedDiscrepancy(t, entity.DiscrepancyOpen, "open one")
	e.seedDiscrepancy(t, entity.DiscrepancyResolved, "resolved one")

	resp, body := e.request(t, http.MethodGet, "/discrepancies?status=open", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if list := body["discrepancies"].([]any); len(list) != 1 {
		t.Errorf("filtered rows = %d, want 1", len(list))
	}

	resp, _ = e.request(t, http.MethodGet, "/discrepancies?status=bogus", true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bogus status → %d, want 400", resp.StatusCode)
	}

	resp, _ = e.request(t, http.MethodGet, "/discrepancies", false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthed → %d, want 401", resp.StatusCode)
	}
}

func TestPatchDiscrepancy_E2E(t *testing.T) {
	e := newEnv(t)
	d := e.seedDiscrepancy(t, entity.DiscrepancyOpen, "Price raised")

	resp, body := e.patchJSON(t, "/discrepancies/"+d.ID.String(), `{"status":"resolved"}`, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if body["status"] != "resolved" || body["id"] != d.ID.String() {
		t.Errorf("body = %v", body)
	}

	resp, _ = e.patchJSON(t, "/discrepancies/"+uuid.NewString(), `{"status":"resolved"}`, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id → %d, want 404", resp.StatusCode)
	}

	resp, _ = e.patchJSON(t, "/discrepancies/"+d.ID.String(), `{"status":"archived"}`, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid status → %d, want 400", resp.StatusCode)
	}

	resp, _ = e.patchJSON(t, "/discrepancies/not-a-uuid", `{"status":"resolved"}`, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("malformed id → %d, want 404", resp.StatusCode)
	}
}
