// Package drive is a dumb adapter over the Microsoft Graph OneDrive API —
// Otto's destination-resolution read side. It never touches the DB and
// holds no business decisions.
package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/thirdeye/overseer/internal/entity"
)

const defaultBaseURL = "https://graph.microsoft.com/v1.0"

// maxItems caps every listing: candidates feed an LLM ranking call, so more
// than a page of results is noise.
const maxItems = 10

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

type driveItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	WebURL string `json:"webUrl"`
}

type listResponse struct {
	Value []driveItem `json:"value"`
}

// Search runs OneDrive's search — file names and content both match
// (spec: q='4471' hits file content too).
func (c *Client) Search(ctx context.Context, accessToken, query string) ([]entity.DriveItem, error) {
	// Single quotes double inside the OData literal; the rest URL-escapes.
	escaped := url.PathEscape(strings.ReplaceAll(query, "'", "''"))
	reqURL := fmt.Sprintf("%s/me/drive/search(q='%s')?%s", c.BaseURL, escaped, selectQuery())
	return c.list(ctx, accessToken, reqURL, "search")
}

// Recent lists the user's recently used drive items — the no-query fallback
// for cold starts where nothing matches the supplier or PO number.
func (c *Client) Recent(ctx context.Context, accessToken string) ([]entity.DriveItem, error) {
	return c.list(ctx, accessToken, c.BaseURL+"/me/drive/recent?"+selectQuery(), "recent")
}

// GetItem fetches one drive item — RecordOrder's webUrl for the
// "Recorded ✓ — open sheet" card.
func (c *Client) GetItem(ctx context.Context, accessToken, itemID string) (entity.DriveItem, error) {
	q := url.Values{}
	q.Set("$select", "id,name,webUrl")
	reqURL := c.BaseURL + "/me/drive/items/" + url.PathEscape(itemID) + "?" + q.Encode()

	raw, err := c.get(ctx, accessToken, reqURL, "get item")
	if err != nil {
		return entity.DriveItem{}, err
	}
	var di driveItem
	if err := json.Unmarshal(raw, &di); err != nil {
		return entity.DriveItem{}, fmt.Errorf("drive: decode item: %w", err)
	}
	return toEntity(di), nil
}

func selectQuery() string {
	q := url.Values{}
	q.Set("$select", "id,name,webUrl")
	q.Set("$top", fmt.Sprintf("%d", maxItems))
	return q.Encode()
}

func (c *Client) list(ctx context.Context, accessToken, reqURL, op string) ([]entity.DriveItem, error) {
	raw, err := c.get(ctx, accessToken, reqURL, op)
	if err != nil {
		return nil, err
	}
	var page listResponse
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, fmt.Errorf("drive: decode %s response: %w", op, err)
	}
	out := make([]entity.DriveItem, 0, len(page.Value))
	for _, di := range page.Value {
		out = append(out, toEntity(di))
		if len(out) == maxItems {
			break
		}
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, accessToken, reqURL, op string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("drive: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("drive: %s: %w", op, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("drive: %s: status %d: %s", op, resp.StatusCode, body)
	}
	return io.ReadAll(resp.Body)
}

func toEntity(di driveItem) entity.DriveItem {
	return entity.DriveItem{ID: di.ID, Name: di.Name, WebURL: di.WebURL}
}
