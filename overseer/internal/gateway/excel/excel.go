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
	ID   string `json:"id"`
	Name string `json:"name"`
}

type tablesResponse struct {
	Value []workbookTable `json:"value"`
}

type columnsResponse struct {
	Value []workbookColumn `json:"value"`
}

// GetWorkbookStructure reads the workbook's tables and their column headers
// — the only context PlanSheetAppend needs; data rows are never fetched.
// Columns are fetched per table: the workbook API silently ignores
// $expand=columns on the tables list (200, no columns field, no error).
func (c *Client) GetWorkbookStructure(ctx context.Context, accessToken, itemID string) (entity.WorkbookStructure, error) {
	tablesURL := c.BaseURL + "/me/drive/items/" + url.PathEscape(itemID) + "/workbook/tables?" +
		url.Values{"$select": {"id,name"}}.Encode()
	var tables tablesResponse
	if err := c.getJSON(ctx, accessToken, tablesURL, "list tables", &tables); err != nil {
		return entity.WorkbookStructure{}, err
	}

	structure := entity.WorkbookStructure{Tables: make([]entity.WorkbookTable, 0, len(tables.Value))}
	for _, tbl := range tables.Value {
		colsURL := c.BaseURL + "/me/drive/items/" + url.PathEscape(itemID) +
			"/workbook/tables/" + url.PathEscape(tbl.ID) + "/columns?" +
			url.Values{"$select": {"name"}}.Encode()
		var cols columnsResponse
		if err := c.getJSON(ctx, accessToken, colsURL, "list columns", &cols); err != nil {
			return entity.WorkbookStructure{}, err
		}
		// A real Excel table always has ≥1 column; empty means the API
		// misbehaved — fail here rather than plan an empty row.
		if len(cols.Value) == 0 {
			return entity.WorkbookStructure{}, fmt.Errorf("excel: table %q returned no columns", tbl.Name)
		}
		names := make([]string, 0, len(cols.Value))
		for _, col := range cols.Value {
			names = append(names, col.Name)
		}
		structure.Tables = append(structure.Tables, entity.WorkbookTable{Name: tbl.Name, Columns: names})
	}
	return structure, nil
}

func (c *Client) getJSON(ctx context.Context, accessToken, reqURL, op string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("excel: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("excel: %s: %w", op, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("excel: %s: status %d: %s", op, resp.StatusCode, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("excel: decode %s: %w", op, err)
	}
	return nil
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
