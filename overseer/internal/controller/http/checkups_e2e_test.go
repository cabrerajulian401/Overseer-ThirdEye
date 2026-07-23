package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

func (e *env) checkupJSON(t *testing.T, method, path, body string, authed bool) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
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

const validCheckupBody = `{
	"conversationId": "conv-1",
	"companyName": "Delmar Tooling",
	"emailAddress": "dana@delmar.com",
	"orderId": 4471,
	"subject": "Follow-up on PO 4471",
	"body": "Hi Dana, any update?",
	"sendDate": "2026-07-15T09:00:00Z",
	"currentEstDate": "2026-07-20",
	"cancelIfReply": true,
	"repeatFlow": 3,
	"repeatEveryDays": 7
}`

func TestPostCheckups_E2E(t *testing.T) {
	e := newEnv(t)

	resp, body := e.checkupJSON(t, http.MethodPost, "/checkups", validCheckupBody, true)

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %v)", resp.StatusCode, body)
	}
	if body["flowId"] == nil || body["flowId"] == "" {
		t.Error("missing flowId")
	}
	list, ok := body["checkups"].([]any)
	if !ok || len(list) != 3 {
		t.Fatalf("checkups = %v, want 3 rows", body["checkups"])
	}

	first := list[0].(map[string]any)
	for _, key := range []string{"id", "flowId", "companyName", "emailAddress", "orderId", "status", "subject", "sendDate", "cancelIfReply", "repeatFlow", "dateCreated"} {
		if _, ok := first[key]; !ok {
			t.Errorf("missing camelCase key %q in %v", key, first)
		}
	}
	if first["status"] != "pending" || first["companyName"] != "Delmar Tooling" {
		t.Errorf("first row = %v", first)
	}
	// cadence: send dates step by repeatEveryDays
	wantDates := []string{"2026-07-15T09:00:00Z", "2026-07-22T09:00:00Z", "2026-07-29T09:00:00Z"}
	for i, item := range list {
		row := item.(map[string]any)
		if row["sendDate"] != wantDates[i] {
			t.Errorf("row %d sendDate = %v, want %s", i, row["sendDate"], wantDates[i])
		}
		if row["flowId"] != body["flowId"] {
			t.Errorf("row %d flowId = %v, want %v", i, row["flowId"], body["flowId"])
		}
	}
	if e.checkups.Count() != 3 {
		t.Errorf("repo rows = %d, want 3", e.checkups.Count())
	}
}

func TestPostCheckups_E2E_ServerMintsIDsAndIgnoresSmuggledOwner(t *testing.T) {
	e := newEnv(t)
	smuggledID := uuid.NewString()
	smuggledUser := uuid.NewString()
	body := `{
		"id": "` + smuggledID + `",
		"flowId": "` + smuggledID + `",
		"userId": "` + smuggledUser + `",
		"companyName": "Delmar Tooling",
		"emailAddress": "dana@delmar.com",
		"subject": "Follow-up",
		"body": "Any update?",
		"sendDate": "2026-07-15T09:00:00Z"
	}`

	resp, out := e.checkupJSON(t, http.MethodPost, "/checkups", body, true)

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %v)", resp.StatusCode, out)
	}
	if out["flowId"] == smuggledID {
		t.Error("client-sent flowId was honored; server must mint ids")
	}
	rows := e.checkups.All()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].ID.String() == smuggledID {
		t.Error("client-sent id was honored")
	}
	if rows[0].UserID != e.user.ID || rows[0].AccountID != e.user.AccountID {
		t.Errorf("owner = %s/%s, want the token's user %s/%s", rows[0].AccountID, rows[0].UserID, e.user.AccountID, e.user.ID)
	}
}

func TestPostCheckups_E2E_Errors(t *testing.T) {
	e := newEnv(t)

	missing := `{"companyName":"Delmar","subject":"s","body":"b","sendDate":"2026-07-15T09:00:00Z"}`
	if resp, _ := e.checkupJSON(t, http.MethodPost, "/checkups", missing, true); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing emailAddress → %d, want 400", resp.StatusCode)
	}

	badDate := strings.Replace(validCheckupBody, "2026-07-15T09:00:00Z", "next tuesday", 1)
	if resp, _ := e.checkupJSON(t, http.MethodPost, "/checkups", badDate, true); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad sendDate → %d, want 400", resp.StatusCode)
	}

	noInterval := strings.Replace(validCheckupBody, `"repeatEveryDays": 7`, `"repeatEveryDays": 0`, 1)
	if resp, _ := e.checkupJSON(t, http.MethodPost, "/checkups", noInterval, true); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("repeatFlow 3 without interval → %d, want 400", resp.StatusCode)
	}

	if resp, _ := e.checkupJSON(t, http.MethodPost, "/checkups", validCheckupBody, false); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthed → %d, want 401", resp.StatusCode)
	}
	if e.checkups.Count() != 0 {
		t.Errorf("rows leaked from rejected requests: %d", e.checkups.Count())
	}
}

func (e *env) seedCheckup(t *testing.T, status entity.CheckupStatus, sendDate time.Time) entity.CheckupMessage {
	t.Helper()
	stored, err := e.checkups.InsertFlow(context.Background(), []entity.CheckupMessage{{
		AccountID: e.user.AccountID, UserID: e.user.ID, FlowID: uuid.New(),
		ConversationID: "conv-1", CompanyName: "Delmar Tooling", EmailAddress: "dana@delmar.com",
		OrderID: 4471, Status: status, Subject: "Follow-up", Body: "Any update?",
		SendDate: sendDate, RepeatFlow: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return stored[0]
}

func TestGetCheckups_E2E(t *testing.T) {
	e := newEnv(t)
	base := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	e.seedCheckup(t, entity.CheckupPending, base.Add(24*time.Hour))
	e.seedCheckup(t, entity.CheckupPending, base)
	e.seedCheckup(t, entity.CheckupCancelled, base.Add(-time.Hour))

	resp, body := e.checkupJSON(t, http.MethodGet, "/checkups", "", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	list, ok := body["checkups"].([]any)
	if !ok || len(list) != 3 {
		t.Fatalf("checkups = %v, want 3", body["checkups"])
	}
	// soonest send_date first
	first := list[0].(map[string]any)
	if first["status"] != "cancelled" {
		t.Errorf("first row = %v, want the earliest send date", first)
	}

	resp, body = e.checkupJSON(t, http.MethodGet, "/checkups?status=pending", "", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("filtered status = %d, want 200", resp.StatusCode)
	}
	if list, _ := body["checkups"].([]any); len(list) != 2 {
		t.Errorf("pending rows = %d, want 2", len(list))
	}

	if resp, _ := e.checkupJSON(t, http.MethodGet, "/checkups?status=snoozed", "", true); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid status filter → %d, want 400", resp.StatusCode)
	}
	if resp, _ := e.checkupJSON(t, http.MethodGet, "/checkups", "", false); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthed → %d, want 401", resp.StatusCode)
	}
}

func TestPatchCheckup_E2E(t *testing.T) {
	e := newEnv(t)
	row := e.seedCheckup(t, entity.CheckupPending, time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC))
	path := "/checkups/" + row.ID.String()

	// pause
	resp, body := e.checkupJSON(t, http.MethodPatch, path, `{"status":"paused"}`, true)
	if resp.StatusCode != http.StatusOK || body["status"] != "paused" {
		t.Fatalf("pause = %d %v, want 200 paused", resp.StatusCode, body)
	}
	// resume
	resp, body = e.checkupJSON(t, http.MethodPatch, path, `{"status":"pending"}`, true)
	if resp.StatusCode != http.StatusOK || body["status"] != "pending" {
		t.Fatalf("resume = %d %v, want 200 pending", resp.StatusCode, body)
	}
	// cancel
	resp, body = e.checkupJSON(t, http.MethodPatch, path, `{"status":"cancelled"}`, true)
	if resp.StatusCode != http.StatusOK || body["status"] != "cancelled" {
		t.Fatalf("cancel = %d %v, want 200 cancelled", resp.StatusCode, body)
	}
	// cancelled is terminal
	if resp, _ = e.checkupJSON(t, http.MethodPatch, path, `{"status":"pending"}`, true); resp.StatusCode != http.StatusConflict {
		t.Errorf("patch cancelled row → %d, want 409", resp.StatusCode)
	}
}

func TestPatchCheckup_E2E_Errors(t *testing.T) {
	e := newEnv(t)
	sent := e.seedCheckup(t, entity.CheckupSent, time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC))

	if resp, _ := e.checkupJSON(t, http.MethodPatch, "/checkups/"+sent.ID.String(), `{"status":"paused"}`, true); resp.StatusCode != http.StatusConflict {
		t.Errorf("patch sent row → %d, want 409", resp.StatusCode)
	}
	if resp, _ := e.checkupJSON(t, http.MethodPatch, "/checkups/"+uuid.NewString(), `{"status":"paused"}`, true); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id → %d, want 404", resp.StatusCode)
	}
	if resp, _ := e.checkupJSON(t, http.MethodPatch, "/checkups/not-a-uuid", `{"status":"paused"}`, true); resp.StatusCode != http.StatusNotFound {
		t.Errorf("malformed id → %d, want 404", resp.StatusCode)
	}
	pending := e.seedCheckup(t, entity.CheckupPending, time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC))
	if resp, _ := e.checkupJSON(t, http.MethodPatch, "/checkups/"+pending.ID.String(), `{"status":"sent"}`, true); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("client setting sent → %d, want 400", resp.StatusCode)
	}
	if resp, _ := e.checkupJSON(t, http.MethodPatch, "/checkups/"+pending.ID.String(), `{"status":"paused"}`, false); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthed → %d, want 401", resp.StatusCode)
	}
}
