// Package graph is a dumb adapter over the Microsoft Graph mail API. It never
// touches the DB and holds no business decisions — it lists messages and maps
// them to entities.
package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/thirdeye/overseer/internal/entity"
)

const defaultBaseURL = "https://graph.microsoft.com/v1.0"

// pageSize is Graph's $top per page; pagination follows @odata.nextLink.
const pageSize = 50

// maxMessages bounds the initial sync — a month of a busy inbox through an
// LLM is slow and expensive, so cap the fan-out.
const maxMessages = 1000

type Client struct {
	HTTPClient *http.Client
	BaseURL    string
}

func New() *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		BaseURL:    defaultBaseURL,
	}
}

// graphMessage mirrors the Graph message resource fields we select.
type graphMessage struct {
	ID                string `json:"id"`
	ConversationID    string `json:"conversationId"`
	ConversationIndex string `json:"conversationIndex"`
	Subject           string `json:"subject"`
	BodyPreview       string `json:"bodyPreview"`
	WebLink           string `json:"webLink"`
	CreatedDateTime   string `json:"createdDateTime"`
	Body              struct {
		Content string `json:"content"`
	} `json:"body"`
	From struct {
		EmailAddress struct {
			Name    string `json:"name"`
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"from"`
}

type listResponse struct {
	Value    []graphMessage `json:"value"`
	NextLink string         `json:"@odata.nextLink"`
}

// ListMessages fetches the user's messages received since the given time,
// newest first, following pagination up to maxMessages.
func (c *Client) ListMessages(ctx context.Context, accessToken string, since time.Time) ([]entity.EmailMessage, error) {
	q := url.Values{}
	q.Set("$select", "id,conversationId,conversationIndex,from,subject,body,bodyPreview,webLink,createdDateTime")
	q.Set("$filter", fmt.Sprintf("receivedDateTime ge %s", since.UTC().Format(time.RFC3339)))
	q.Set("$orderby", "receivedDateTime desc")
	q.Set("$top", fmt.Sprintf("%d", pageSize))
	next := c.BaseURL + "/me/messages?" + q.Encode()

	var out []entity.EmailMessage
	for next != "" && len(out) < maxMessages {
		page, err := c.fetchPage(ctx, accessToken, next)
		if err != nil {
			return nil, err
		}
		for _, gm := range page.Value {
			out = append(out, toEntity(gm))
		}
		next = page.NextLink
	}
	return out, nil
}

func (c *Client) fetchPage(ctx context.Context, accessToken, pageURL string) (*listResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("graph: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("graph: list messages: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("graph: list messages: status %d: %s", resp.StatusCode, body)
	}

	var page listResponse
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("graph: decode response: %w", err)
	}
	return &page, nil
}

// subscriptionRequest mirrors Graph's POST /subscriptions body.
type subscriptionRequest struct {
	ChangeType         string `json:"changeType"`
	NotificationURL    string `json:"notificationUrl"`
	Resource           string `json:"resource"`
	ExpirationDateTime string `json:"expirationDateTime"`
	ClientState        string `json:"clientState"`
}

type subscriptionResponse struct {
	ID string `json:"id"`
}

// CreateSubscription registers a change-notification webhook for new inbox
// messages and returns Microsoft's subscription id. Graph validates
// notificationURL synchronously during this call (the validationToken echo).
func (c *Client) CreateSubscription(ctx context.Context, accessToken, notificationURL, clientState string, expires time.Time) (string, error) {
	payload, err := json.Marshal(subscriptionRequest{
		ChangeType:         "created",
		NotificationURL:    notificationURL,
		Resource:           "/me/messages",
		ExpirationDateTime: expires.UTC().Format(time.RFC3339),
		ClientState:        clientState,
	})
	if err != nil {
		return "", fmt.Errorf("graph: marshal subscription: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/subscriptions", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("graph: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("graph: create subscription: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("graph: create subscription: status %d: %s", resp.StatusCode, body)
	}

	var out subscriptionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("graph: decode subscription: %w", err)
	}
	return out.ID, nil
}

// RenewSubscription pushes a subscription's expiry forward (~3-day max
// lifetime per Graph; the renewal worker calls this before expiry).
func (c *Client) RenewSubscription(ctx context.Context, accessToken, subscriptionID string, expires time.Time) error {
	payload, err := json.Marshal(map[string]string{
		"expirationDateTime": expires.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("graph: marshal renewal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		c.BaseURL+"/subscriptions/"+url.PathEscape(subscriptionID), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("graph: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("graph: renew subscription: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("graph: renew subscription: status %d: %s", resp.StatusCode, body)
	}
	return nil
}

// GetMessage fetches one message by Graph id — the webhook job's read path
// (spec: gateway/graph/GetMessage).
func (c *Client) GetMessage(ctx context.Context, accessToken, messageID string) (entity.EmailMessage, error) {
	q := url.Values{}
	q.Set("$select", "id,conversationId,conversationIndex,from,subject,body,bodyPreview,webLink,createdDateTime")
	reqURL := c.BaseURL + "/me/messages/" + url.PathEscape(messageID) + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return entity.EmailMessage{}, fmt.Errorf("graph: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return entity.EmailMessage{}, fmt.Errorf("graph: get message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return entity.EmailMessage{}, fmt.Errorf("graph: get message: status %d: %s", resp.StatusCode, body)
	}

	var gm graphMessage
	if err := json.NewDecoder(resp.Body).Decode(&gm); err != nil {
		return entity.EmailMessage{}, fmt.Errorf("graph: decode message: %w", err)
	}
	return toEntity(gm), nil
}

// ListConversationMessages fetches every message in a conversation — the
// Audit read path: history is re-fetched from Graph at audit time, never
// stored. Results are sorted oldest → newest (the entity.Thread convention)
// here, because Graph rejects $orderby combined with the conversationId
// $filter (InefficientFilter).
func (c *Client) ListConversationMessages(ctx context.Context, accessToken, conversationID string) ([]entity.EmailMessage, error) {
	q := url.Values{}
	q.Set("$select", "id,conversationId,conversationIndex,from,subject,body,bodyPreview,webLink,createdDateTime")
	q.Set("$filter", fmt.Sprintf("conversationId eq '%s'", strings.ReplaceAll(conversationID, "'", "''")))
	q.Set("$top", fmt.Sprintf("%d", pageSize))
	next := c.BaseURL + "/me/messages?" + q.Encode()

	var out []entity.EmailMessage
	for next != "" && len(out) < maxMessages {
		page, err := c.fetchPage(ctx, accessToken, next)
		if err != nil {
			return nil, err
		}
		for _, gm := range page.Value {
			out = append(out, toEntity(gm))
		}
		next = page.NextLink
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedDateTime.Before(out[j].CreatedDateTime) })
	return out, nil
}

func toEntity(gm graphMessage) entity.EmailMessage {
	created, _ := time.Parse(time.RFC3339, gm.CreatedDateTime)
	return entity.EmailMessage{
		ID:                gm.ID,
		ConversationID:    gm.ConversationID,
		ConversationIndex: gm.ConversationIndex,
		From:              gm.From.EmailAddress.Address,
		FromName:          gm.From.EmailAddress.Name,
		Subject:           gm.Subject,
		Body:              gm.Body.Content,
		BodyPreview:       gm.BodyPreview,
		WebLink:           gm.WebLink,
		CreatedDateTime:   created,
	}
}
