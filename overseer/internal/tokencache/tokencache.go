// Package tokencache holds the last Graph access token seen per user. Graph
// change notifications carry no token, but the webhook job must call Graph on
// the user's behalf — the auth middleware fills this cache on every authed
// request, and the job reads from it. In-memory MVP; the durable
// refresh-token flow (users.graph_refresh_token_enc) replaces this later.
package tokencache

import (
	"sync"

	"github.com/google/uuid"
)

type Cache struct {
	mu     sync.RWMutex
	tokens map[uuid.UUID]string
}

func New() *Cache {
	return &Cache{tokens: make(map[uuid.UUID]string)}
}

// Put stores the most recent token for the user.
func (c *Cache) Put(userID uuid.UUID, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens[userID] = token
}

// GraphToken returns the last token seen for the user, if any.
func (c *Cache) GraphToken(userID uuid.UUID) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.tokens[userID]
	return t, ok
}
