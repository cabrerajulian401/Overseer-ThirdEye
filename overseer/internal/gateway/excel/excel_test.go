package excel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The workbook API silently IGNORES $expand=columns — observed live: the
// response echoes the projection in @odata.context but embeds no columns
// field — so the client must list tables, then fetch each table's columns
// with a separate GET /tables/{id}/columns call.
func TestGetWorkbookStructure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok-123" {
			t.Errorf("auth header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/me/drive/items/item-1/workbook/tables":
			fmt.Fprint(w, `{"value": [
				{"id": "{00000000-000C-0000-FFFF-FFFF00000000}", "name": "PO_Log"},
				{"id": "{00000000-000C-0000-FFFF-FFFF00000001}", "name": "Contacts"}
			]}`)
		case "/me/drive/items/item-1/workbook/tables/{00000000-000C-0000-FFFF-FFFF00000000}/columns":
			fmt.Fprint(w, `{"value": [{"id": "1", "name": "PO"}, {"id": "2", "name": "Supplier"}, {"id": "3", "name": "Price"}]}`)
		case "/me/drive/items/item-1/workbook/tables/{00000000-000C-0000-FFFF-FFFF00000001}/columns":
			fmt.Fprint(w, `{"value": [{"id": "1", "name": "Name"}, {"id": "2", "name": "Email"}]}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	structure, err := c.GetWorkbookStructure(context.Background(), "tok-123", "item-1")
	if err != nil {
		t.Fatalf("GetWorkbookStructure: %v", err)
	}
	if len(structure.Tables) != 2 {
		t.Fatalf("tables = %d, want 2", len(structure.Tables))
	}
	first := structure.Tables[0]
	if first.Name != "PO_Log" || len(first.Columns) != 3 || first.Columns[1] != "Supplier" {
		t.Errorf("table = %+v", first)
	}
	second := structure.Tables[1]
	if second.Name != "Contacts" || len(second.Columns) != 2 || second.Columns[1] != "Email" {
		t.Errorf("table = %+v", second)
	}
}

// A real Excel table always has at least one column; an empty columns
// response means the API misbehaved. Fail loudly — an empty structure
// reaching the plan LLM produces an empty row that Graph then rejects.
func TestGetWorkbookStructureNoColumns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/me/drive/items/item-1/workbook/tables":
			fmt.Fprint(w, `{"value": [{"id": "{00000000-000C-0000-FFFF-FFFF00000000}", "name": "PO_Log"}]}`)
		default:
			fmt.Fprint(w, `{"value": []}`)
		}
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	_, err := c.GetWorkbookStructure(context.Background(), "tok", "item-1")
	if err == nil || !strings.Contains(err.Error(), "PO_Log") {
		t.Fatalf("err = %v, want error naming the empty table", err)
	}
}

func TestAppendTableRow(t *testing.T) {
	var gotBody map[string][][]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := "/me/drive/items/item-1/workbook/tables/PO_Log/rows/add"; r.URL.Path != want || r.Method != http.MethodPost {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, want)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"index": 12}`)
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	err := c.AppendTableRow(context.Background(), "tok", "item-1", "PO_Log", []string{"4471", "Delmar Tooling", "$42.50/unit"})
	if err != nil {
		t.Fatalf("AppendTableRow: %v", err)
	}
	rows := gotBody["values"]
	if len(rows) != 1 || len(rows[0]) != 3 || rows[0][0] != "4471" || rows[0][1] != "Delmar Tooling" {
		t.Errorf("body values = %+v", rows)
	}
}

func TestAppendTableRowNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"ItemNotFound"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	if err := c.AppendTableRow(context.Background(), "tok", "gone", "PO_Log", []string{"x"}); err == nil {
		t.Fatal("want error on 404, got nil")
	}
}
