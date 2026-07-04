package runpod

import (
	"context"
	"encoding/json"
	"fmt"
)

// ListNetworkVolumes lists all network volumes on the account.
func (c *Client) ListNetworkVolumes(ctx context.Context) ([]NetworkVolume, error) {
	// Accept both documented shapes: bare array and object wrapper.
	var raw json.RawMessage
	if err := c.Get(ctx, "/networkvolumes", &raw); err != nil {
		return nil, fmt.Errorf("failed to list network volumes: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}

	var volumes []NetworkVolume
	if err := json.Unmarshal(raw, &volumes); err == nil {
		return volumes, nil
	}

	var wrapped struct {
		NetworkVolumes []NetworkVolume `json:"networkVolumes"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.NetworkVolumes != nil {
		return wrapped.NetworkVolumes, nil
	}

	return nil, fmt.Errorf("failed to list network volumes: unexpected response shape")
}

// GetNetworkVolume retrieves a network volume by ID.
func (c *Client) GetNetworkVolume(ctx context.Context, volumeID string) (*NetworkVolume, error) {
	if err := c.validateRequired("volumeID", volumeID); err != nil {
		return nil, err
	}

	var volume NetworkVolume
	if err := c.Get(ctx, "/networkvolumes/"+volumeID, &volume); err != nil {
		return nil, fmt.Errorf("failed to get network volume %s: %w", volumeID, err)
	}
	return &volume, nil
}

// CreateNetworkVolume creates a new network volume.
func (c *Client) CreateNetworkVolume(ctx context.Context, req *CreateNetworkVolumeRequest) (*NetworkVolume, error) {
	if req == nil {
		return nil, NewValidationError("request", "cannot be nil")
	}
	if err := c.validateRequired("name", req.Name); err != nil {
		return nil, err
	}
	if err := c.validatePositive("size", req.Size); err != nil {
		return nil, err
	}
	if err := c.validateRequired("dataCenterId", req.DataCenterID); err != nil {
		return nil, err
	}

	var volume NetworkVolume
	if err := c.Post(ctx, "/networkvolumes", req, &volume); err != nil {
		return nil, fmt.Errorf("failed to create network volume: %w", err)
	}
	return &volume, nil
}

// UpdateNetworkVolume updates a network volume's name and/or size (resize
// can only grow — RunPod rejects shrinks).
func (c *Client) UpdateNetworkVolume(ctx context.Context, volumeID string, req *UpdateNetworkVolumeRequest) (*NetworkVolume, error) {
	if err := c.validateRequired("volumeID", volumeID); err != nil {
		return nil, err
	}
	if req == nil || (req.Name == "" && req.Size == 0) {
		return nil, NewValidationError("request", "must set name and/or size")
	}
	if req.Size != 0 {
		if err := c.validatePositive("size", req.Size); err != nil {
			return nil, err
		}
	}

	var volume NetworkVolume
	if err := c.Patch(ctx, "/networkvolumes/"+volumeID, req, &volume); err != nil {
		return nil, fmt.Errorf("failed to update network volume %s: %w", volumeID, err)
	}
	return &volume, nil
}

// DeleteNetworkVolume deletes a network volume by ID.
func (c *Client) DeleteNetworkVolume(ctx context.Context, volumeID string) error {
	if err := c.validateRequired("volumeID", volumeID); err != nil {
		return err
	}
	if err := c.Delete(ctx, "/networkvolumes/"+volumeID); err != nil {
		return fmt.Errorf("failed to delete network volume %s: %w", volumeID, err)
	}
	return nil
}
