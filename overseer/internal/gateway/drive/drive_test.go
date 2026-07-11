package drive

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

const itemsJSON = `{"value": [
	{"id": "item-1", "name": "Orders 2026.xlsx", "webUrl": "https://onedrive.example/orders-2026"},
	{"id": "item-2", "name": "PO_Log.xlsx", "webUrl": "https://onedrive.example/po-log"}
]}`

func TestSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// single quote in the query must be doubled inside the OData literal
		if want := "/me/drive/search(q='Delmar''s')"; r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-123" {
			t.Errorf("auth header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, itemsJSON)
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	items, err := c.Search(context.Background(), "tok-123", "Delmar's")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 2 || items[0].ID != "item-1" || items[0].Name != "Orders 2026.xlsx" ||
		items[0].WebURL != "https://onedrive.example/orders-2026" {
		t.Errorf("items = %+v", items)
	}
}

func TestRecent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/drive/recent" {
			t.Errorf("path = %q, want /me/drive/recent", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, itemsJSON)
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	items, err := c.Recent(context.Background(), "tok")
	if err != nil || len(items) != 2 {
		t.Fatalf("Recent = %v, %v", items, err)
	}
}

func TestGetItem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/drive/items/item-1" {
			t.Errorf("path = %q, want /me/drive/items/item-1", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id": "item-1", "name": "Orders 2026.xlsx", "webUrl": "https://onedrive.example/orders-2026"}`)
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	item, err := c.GetItem(context.Background(), "tok", "item-1")
	if err != nil || item.WebURL != "https://onedrive.example/orders-2026" {
		t.Fatalf("GetItem = %+v, %v", item, err)
	}
}

func TestSearchNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	if _, err := c.Search(context.Background(), "tok", "orders"); err == nil {
		t.Fatal("want error on 403, got nil")
	}
}
