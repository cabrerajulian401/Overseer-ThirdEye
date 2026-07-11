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

func (e *env) postOttoJSON(t *testing.T, path, body string, authed bool) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+path, strings.NewReader(body))
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

func (e *env) seedOttoThread(t *testing.T) {
	t.Helper()
	e.graph.ConversationsByID = map[string][]entity.EmailMessage{
		"conv-1": {
			{ID: "msg-1", ConversationID: "conv-1", CreatedDateTime: time.Now().Add(-time.Hour)},
			{ID: "msg-2", ConversationID: "conv-1", WebLink: "https://outlook.example/msg-2", CreatedDateTime: time.Now()},
		},
	}
	e.llm.Extractions = map[string]entity.OrderExtraction{
		"conv-1": {CompanyName: "Delmar Tooling", OrderID: 4471, Status: entity.OrderStatusConfirmed},
	}
}

func ottoDest(id, name string) entity.DriveItem {
	return entity.DriveItem{ID: id, Name: name, WebURL: "https://onedrive.example/" + id}
}

func TestPostOttoScans_E2E(t *testing.T) {
	e := newEnv(t)
	e.seedOttoThread(t)
	e.drive.SearchResults["Delmar Tooling"] = []entity.DriveItem{ottoDest("item-1", "Delmar orders.xlsx")}
	e.llm.RankResult = []entity.DriveItem{
		ottoDest("item-1", "Delmar orders.xlsx"),
		ottoDest("item-2", "PO_Log.xlsx"),
		ottoDest("item-3", "Orders 2026.xlsx"),
	}

	resp, body := e.postOttoJSON(t, "/otto/scans", `{"messageId":"msg-2","conversationId":"conv-1"}`, true)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if body["scanId"] == nil || body["scanId"] == "" {
		t.Error("missing scanId")
	}
	fields, ok := body["extractedFields"].(map[string]any)
	if !ok || fields["companyName"] != "Delmar Tooling" || fields["orderId"] != float64(4471) {
		t.Errorf("extractedFields = %v", body["extractedFields"])
	}
	dests, ok := body["destinations"].([]any)
	if !ok || len(dests) != 3 {
		t.Fatalf("destinations = %v", body["destinations"])
	}
	first := dests[0].(map[string]any)
	if first["id"] != "item-1" || first["name"] != "Delmar orders.xlsx" || first["webUrl"] != "https://onedrive.example/item-1" {
		t.Errorf("destination = %v", first)
	}
	// side effects: the calls that WOULD have hit Graph/LLM
	if e.llm.ExtractCalls != 1 || e.llm.RankCalls != 1 {
		t.Errorf("extract=%d rank=%d, want 1, 1", e.llm.ExtractCalls, e.llm.RankCalls)
	}
	if e.otto.Count() != 1 {
		t.Errorf("scan rows = %d, want 1", e.otto.Count())
	}
}

func TestPostOttoScans_E2E_Errors(t *testing.T) {
	e := newEnv(t)
	e.seedOttoThread(t)
	e.llm.ExtractErr = context.DeadlineExceeded // any upstream failure

	resp, _ := e.postOttoJSON(t, "/otto/scans", `{"messageId":"msg-2","conversationId":"conv-1"}`, true)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("LLM failure → %d, want 502", resp.StatusCode)
	}
	if e.otto.Count() != 0 {
		t.Error("scan row leaked on failure")
	}

	resp, _ = e.postOttoJSON(t, "/otto/scans", `{"messageId":"msg-2"}`, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing conversationId → %d, want 400", resp.StatusCode)
	}

	resp, _ = e.postOttoJSON(t, "/otto/scans", `{"messageId":"msg-2","conversationId":"conv-1"}`, false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthed → %d, want 401", resp.StatusCode)
	}
}

func (e *env) seedScan(t *testing.T) entity.OttoScan {
	t.Helper()
	scan, err := e.otto.Insert(context.Background(), entity.OttoScan{
		AccountID: e.user.AccountID, UserID: e.user.ID,
		MessageID: "msg-2", ConversationID: "conv-1",
		Fields: entity.OrderExtraction{CompanyName: "Delmar Tooling", OrderID: 4471},
		Status: entity.ScanScanned,
	})
	if err != nil {
		t.Fatal(err)
	}
	return scan
}

func TestPostOttoRecord_E2E(t *testing.T) {
	e := newEnv(t)
	scan := e.seedScan(t)
	e.excel.Structures["item-1"] = entity.WorkbookStructure{Tables: []entity.WorkbookTable{
		{Name: "PO_Log", Columns: []string{"PO", "Supplier", "ETA"}},
	}}
	e.llm.PlanResult = entity.SheetAppendPlan{Table: "PO_Log", Values: []string{"4471", "Delmar Tooling Inc.", ""}}
	e.drive.Items["item-1"] = ottoDest("item-1", "Orders 2026.xlsx")

	// the user EDITED the company name on the confirm card
	resp, body := e.postOttoJSON(t, "/otto/scans/"+scan.ID.String()+"/record",
		`{"selectedResource":"item-1","fields":{"companyName":"Delmar Tooling Inc.","orderId":4471,"status":"confirmed"}}`, true)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if body["webUrl"] != "https://onedrive.example/item-1" || body["status"] != "recorded" {
		t.Errorf("body = %v", body)
	}
	// the plan saw the EDITED fields from the request body — not the stored scan
	if len(e.llm.PlanFieldsSeen) != 1 || e.llm.PlanFieldsSeen[0].CompanyName != "Delmar Tooling Inc." {
		t.Errorf("plan fields = %+v, want the edited company name", e.llm.PlanFieldsSeen)
	}
	// FakeExcel received exactly the planned row
	if len(e.excel.Appends) != 1 || e.excel.Appends[0].Table != "PO_Log" || e.excel.Appends[0].Values[1] != "Delmar Tooling Inc." {
		t.Errorf("appends = %+v", e.excel.Appends)
	}
}

func TestPostOttoRecord_E2E_Errors(t *testing.T) {
	e := newEnv(t)
	scan := e.seedScan(t)
	e.excel.Structures["item-1"] = entity.WorkbookStructure{Tables: []entity.WorkbookTable{
		{Name: "PO_Log", Columns: []string{"PO"}},
	}}
	e.llm.PlanResult = entity.SheetAppendPlan{Table: "PO_Log", Values: []string{"4471"}}
	e.drive.Items["item-1"] = ottoDest("item-1", "Orders 2026.xlsx")

	resp, _ := e.postOttoJSON(t, "/otto/scans/"+uuid.NewString()+"/record",
		`{"selectedResource":"item-1","fields":{}}`, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown scan → %d, want 404", resp.StatusCode)
	}

	resp, _ = e.postOttoJSON(t, "/otto/scans/not-a-uuid/record",
		`{"selectedResource":"item-1","fields":{}}`, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("malformed id → %d, want 404", resp.StatusCode)
	}

	resp, _ = e.postOttoJSON(t, "/otto/scans/"+scan.ID.String()+"/record", `{"fields":{}}`, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing selectedResource → %d, want 400", resp.StatusCode)
	}

	// record once, then again → 409
	if resp, _ = e.postOttoJSON(t, "/otto/scans/"+scan.ID.String()+"/record",
		`{"selectedResource":"item-1","fields":{}}`, true); resp.StatusCode != http.StatusOK {
		t.Fatalf("first record = %d, want 200", resp.StatusCode)
	}
	resp, _ = e.postOttoJSON(t, "/otto/scans/"+scan.ID.String()+"/record",
		`{"selectedResource":"item-1","fields":{}}`, true)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("second record → %d, want 409", resp.StatusCode)
	}
}
