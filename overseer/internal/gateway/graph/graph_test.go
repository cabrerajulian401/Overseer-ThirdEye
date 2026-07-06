package graph

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
