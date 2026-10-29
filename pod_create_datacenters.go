package runpod

import "context"

// ListPodCreateDatacenterIDs reads the live v2 datacenter catalog. Placement
// IDs are unrestricted strings in v2, not an enum in the OpenAPI schema.
func (c *Client) ListPodCreateDatacenterIDs(ctx context.Context) ([]string, error) {
	rows, err := c.ListDataCenters(ctx, nil)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, nil
}
