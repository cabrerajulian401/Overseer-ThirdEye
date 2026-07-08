package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCreateSubscription(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/subscriptions" {
			t.Errorf("got %s %s, want POST /subscriptions", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id": "sub-abc", "expirationDateTime": "2026-07-07T10:00:00Z"}`)
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	expires := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	id, err := c.CreateSubscription(context.Background(), "tok", "https://app.example/webhooks/graph", "secret-state", expires)
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if id != "sub-abc" {
		t.Errorf("id = %q, want sub-abc", id)
	}
	if gotBody["changeType"] != "created" ||
		gotBody["resource"] != "/me/messages" ||
		gotBody["notificationUrl"] != "https://app.example/webhooks/graph" ||
		gotBody["clientState"] != "secret-state" ||
		gotBody["expirationDateTime"] != "2026-07-07T10:00:00Z" {
		t.Errorf("request body = %+v", gotBody)
	}
}

func TestRenewSubscription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/subscriptions/sub-abc" {
			t.Errorf("got %s %s, want PATCH /subscriptions/sub-abc", r.Method, r.URL.Path)
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["expirationDateTime"] != "2026-07-09T10:00:00Z" {
			t.Errorf("body = %+v", body)
		}
		fmt.Fprint(w, `{"id": "sub-abc"}`)
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	expires := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	if err := c.RenewSubscription(context.Background(), "tok", "sub-abc", expires); err != nil {
		t.Fatalf("RenewSubscription: %v", err)
	}
}

func TestGetMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/messages/msg-1" {
			t.Errorf("path = %q, want /me/messages/msg-1", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-123" {
			t.Errorf("auth header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id": "msg-1",
			"conversationId": "conv-9",
			"conversationIndex": "AQHa",
			"subject": "RE: PO 4471",
			"bodyPreview": "New ETA is July 12",
			"webLink": "https://outlook.example/msg-1",
			"createdDateTime": "2026-07-05T10:00:00Z",
			"body": {"content": "Hi, the new ETA is July 12."},
			"from": {"emailAddress": {"name": "Dana Delmar", "address": "dana@delmar.com"}}
		}`)
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	msg, err := c.GetMessage(context.Background(), "tok-123", "msg-1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.ID != "msg-1" || msg.ConversationID != "conv-9" || msg.From != "dana@delmar.com" {
		t.Errorf("mapped message = %+v", msg)
	}
	want := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	if !msg.CreatedDateTime.Equal(want) {
		t.Errorf("CreatedDateTime = %v, want %v", msg.CreatedDateTime, want)
	}
}

func TestListConversationMessages(t *testing.T) {
	var page2 bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/messages" {
			t.Errorf("path = %q, want /me/messages", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-123" {
			t.Errorf("auth header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			page2 = true
			fmt.Fprint(w, `{"value": [{"id": "m-1", "conversationId": "conv'9", "createdDateTime": "2026-07-01T09:00:00Z"}]}`)
			return
		}
		// single quotes in the id must be doubled inside the OData literal
		if got := r.URL.Query().Get("$filter"); got != `conversationId eq 'conv''9'` {
			t.Errorf("$filter = %q", got)
		}
		fmt.Fprintf(w, `{
			"value": [{"id": "m-2", "conversationId": "conv'9", "createdDateTime": "2026-07-03T09:00:00Z"}],
			"@odata.nextLink": %q
		}`, "http://"+r.Host+"/me/messages?page=2")
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	msgs, err := c.ListConversationMessages(context.Background(), "tok-123", "conv'9")
	if err != nil {
		t.Fatalf("ListConversationMessages: %v", err)
	}
	if !page2 {
		t.Error("pagination nextLink not followed")
	}
	if len(msgs) != 2 {
		t.Fatalf("len = %d, want 2", len(msgs))
	}
	// oldest → newest (the entity.Thread convention), sorted client-side
	// because Graph rejects $orderby combined with this $filter.
	if msgs[0].ID != "m-1" || msgs[1].ID != "m-2" {
		t.Errorf("order = %s, %s; want m-1, m-2", msgs[0].ID, msgs[1].ID)
	}
}

func TestGetMessageNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	if _, err := c.GetMessage(context.Background(), "tok", "gone"); err == nil {
		t.Fatal("want error on 404, got nil")
	}
}
