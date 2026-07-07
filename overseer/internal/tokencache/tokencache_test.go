package tokencache

import (
	"testing"

	"github.com/google/uuid"
)

func TestPutAndGet(t *testing.T) {
	c := New()
	userID := uuid.New()

	if _, ok := c.GraphToken(userID); ok {
		t.Fatal("empty cache returned a token")
	}

	c.Put(userID, "tok-1")
	got, ok := c.GraphToken(userID)
	if !ok || got != "tok-1" {
		t.Fatalf("GraphToken = %q, %v", got, ok)
	}

	c.Put(userID, "tok-2") // most recent token wins
	got, _ = c.GraphToken(userID)
	if got != "tok-2" {
		t.Fatalf("GraphToken = %q, want tok-2", got)
	}
}
