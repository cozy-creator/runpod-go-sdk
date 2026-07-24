package runpod

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type podCreateOpenAPIDocument struct {
	Components struct {
		Schemas struct {
			PodCreateInput struct {
				Properties struct {
					DataCenterIDs struct {
						Items struct {
							Enum []string `json:"enum"`
						} `json:"items"`
					} `json:"dataCenterIds"`
				} `json:"properties"`
			} `json:"PodCreateInput"`
		} `json:"schemas"`
	} `json:"components"`
}

// ListPodCreateDatacenterIDs returns the datacenter ids accepted by the REST
// pod-create API. An absent or empty enum is an error so callers fail closed.
func (c *Client) ListPodCreateDatacenterIDs(ctx context.Context) ([]string, error) {
	resp, err := c.makeRequest(ctx, http.MethodGet, "/openapi.json", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get RunPod OpenAPI schema: %w", err)
	}

	var document podCreateOpenAPIDocument
	if err := c.handleResponse(resp, &document); err != nil {
		return nil, fmt.Errorf("failed to decode RunPod OpenAPI schema: %w", err)
	}

	rawIDs := document.Components.Schemas.PodCreateInput.Properties.DataCenterIDs.Items.Enum
	ids := make([]string, 0, len(rawIDs))
	seen := make(map[string]struct{}, cap(ids))
	for _, rawID := range rawIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("RunPod OpenAPI PodCreateInput.dataCenterIds enum is missing or empty")
	}
	return ids, nil
}
