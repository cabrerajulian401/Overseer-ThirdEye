package excel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetWorkbookStructure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := "/me/drive/items/item-1/workbook/tables"; r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-123" {
			t.Errorf("auth header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"value": [
			{"name": "PO_Log", "columns": [{"name": "PO"}, {"name": "Supplier"}, {"name": "Price"}]},
			{"name": "Contacts", "columns": [{"name": "Name"}, {"name": "Email"}]}
		]}`)
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
