package runpod

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// paginatedList refuses incomplete snapshots: absence is valid only after an
// explicit empty list and a complete pagination envelope.
func paginatedList[T any](ctx context.Context, c *Client, endpoint, key string) ([]T, error) {
	return paginatedListRead[T](ctx, c.Get, endpoint, key)
}

func paginatedListRead[T any](ctx context.Context, read func(context.Context, string, any) error, endpoint, key string) ([]T, error) {
	all := make([]T, 0)
	initialURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{initialURL.Query().Get("cursor"): true}
	for {
		var envelope map[string]json.RawMessage
		if err := read(ctx, endpoint, &envelope); err != nil {
			return nil, err
		}
		var rows []T
		raw := envelope[key]
		if len(raw) == 0 || string(raw) == "null" {
			return nil, fmt.Errorf("%s response omitted %s", endpoint, key)
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, fmt.Errorf("decode %s: %w", key, err)
		}
		if rows == nil {
			return nil, fmt.Errorf("%s response returned null %s", endpoint, key)
		}
		var page struct {
			Next json.RawMessage `json:"nextCursor"`
			More *bool           `json:"hasNextPage"`
		}
		if err := json.Unmarshal(envelope["pagination"], &page); err != nil || page.More == nil || len(page.Next) == 0 {
			return nil, fmt.Errorf("%s response omitted valid pagination", endpoint)
		}
		var next *string
		if err := json.Unmarshal(page.Next, &next); err != nil {
			return nil, fmt.Errorf("invalid pagination cursor: %w", err)
		}
		all = append(all, rows...)
		if !*page.More {
			if next != nil {
				return nil, fmt.Errorf("pagination reports a cursor without another page")
			}
			return all, nil
		}
		if len(rows) == 0 || next == nil || *next == "" || seen[*next] {
			return nil, fmt.Errorf("%s pagination did not advance", endpoint)
		}
		seen[*next] = true
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("cursor", *next)
		u.RawQuery = q.Encode()
		endpoint = u.String()
	}
}

func decodeList[T any](ctx context.Context, c *Client, endpoint, key string) ([]T, error) {
	var envelope map[string]json.RawMessage
	if err := c.Get(ctx, endpoint, &envelope); err != nil {
		return nil, err
	}
	raw := envelope[key]
	if len(raw) == 0 || string(raw) == "null" {
		return nil, fmt.Errorf("%s response omitted %s", endpoint, key)
	}
	var rows []T
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode %s: %w", key, err)
	}
	if rows == nil {
		return nil, fmt.Errorf("%s response returned null %s", endpoint, key)
	}
	return rows, nil
}

func validateResourceID(field, id string) error {
	if id == "" || id != strings.TrimSpace(id) {
		return NewValidationError(field, "must be an exact non-empty id")
	}
	return nil
}
func validateResourceIdentity(kind, id, name, requestedID string) error {
	if err := validateResourceID("id", id); err != nil {
		return fmt.Errorf("%s response omitted a valid id", kind)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%s %s response omitted name", kind, id)
	}
	if requestedID != "" && id != requestedID {
		return fmt.Errorf("%s identity mismatch: requested %q, received %q", kind, requestedID, id)
	}
	return nil
}
