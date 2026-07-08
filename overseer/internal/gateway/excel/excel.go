// Package excel is a dumb adapter over the Microsoft Graph Excel workbook
// API — Otto's write side. The usecase writes; the LLM only plans.
package excel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/thirdeye/overseer/internal/entity"
)

const defaultBaseURL = "https://graph.microsoft.com/v1.0"

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

type workbookColumn struct {
	Name string `json:"name"`
}

type workbookTable struct {
	Name    string           `json:"name"`
	Columns []workbookColumn `json:"columns"`
}

type tablesResponse struct {
	Value []workbookTable `json:"value"`
}

// GetWorkbookStructure reads the workbook's tables and their column headers
// — the only context PlanSheetAppend needs; data rows are never fetched.
func (c *Client) GetWorkbookStructure(ctx context.Context, accessToken, itemID string) (entity.WorkbookStructure, error) {
	q := url.Values{}
	q.Set("$select", "name")
	q.Set("$expand", "columns($select=name)")
	reqURL := c.BaseURL + "/me/drive/items/" + url.PathEscape(itemID) + "/workbook/tables?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return entity.WorkbookStructure{}, fmt.Errorf("excel: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return entity.WorkbookStructure{}, fmt.Errorf("excel: list tables: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return entity.WorkbookStructure{}, fmt.Errorf("excel: list tables: status %d: %s", resp.StatusCode, body)
	}

	var out tablesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return entity.WorkbookStructure{}, fmt.Errorf("excel: decode tables: %w", err)
	}

	structure := entity.WorkbookStructure{Tables: make([]entity.WorkbookTable, 0, len(out.Value))}
	for _, tbl := range out.Value {
		cols := make([]string, 0, len(tbl.Columns))
		for _, col := range tbl.Columns {
			cols = append(cols, col.Name)
		}
		structure.Tables = append(structure.Tables, entity.WorkbookTable{Name: tbl.Name, Columns: cols})
	}
	return structure, nil
}

// AppendTableRow appends one row to a workbook table
// (https://learn.microsoft.com/en-us/graph/excel-write-to-workbook).
func (c *Client) AppendTableRow(ctx context.Context, accessToken, itemID, tableName string, values []string) error {
	row := make([]any, len(values))
	for i, v := range values {
		row[i] = v
	}
	payload, err := json.Marshal(map[string]any{"values": [][]any{row}})
	if err != nil {
		return fmt.Errorf("excel: marshal row: %w", err)
	}

	reqURL := c.BaseURL + "/me/drive/items/" + url.PathEscape(itemID) +
		"/workbook/tables/" + url.PathEscape(tableName) + "/rows/add"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("excel: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("excel: append row: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("excel: append row: status %d: %s", resp.StatusCode, body)
	}
	return nil
}
