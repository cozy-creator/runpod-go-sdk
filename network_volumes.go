package runpod

import (
	"context"
	"fmt"
	"net/url"
)

// ListNetworkVolumes lists all network volumes on the account.
func (c *Client) ListNetworkVolumes(ctx context.Context) ([]NetworkVolume, error) {
	return c.listNetworkVolumes(ctx, c.Get)
}

// GetNetworkVolume retrieves a network volume by ID.
func (c *Client) GetNetworkVolume(ctx context.Context, volumeID string) (*NetworkVolume, error) {
	if err := validateResourceID("volumeID", volumeID); err != nil {
		return nil, err
	}

	var volume NetworkVolume
	if err := c.Get(ctx, "/network-volumes/"+url.PathEscape(volumeID), &volume); err != nil {
		return nil, fmt.Errorf("failed to get network volume %s: %w", volumeID, err)
	}
	if err := validateNetworkVolume(volume, volumeID); err != nil {
		return nil, err
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
	if req.Size < 10 || req.Size > 4096 {
		return nil, NewValidationError("size", "must be between 10 and 4096 GB")
	}
	if err := validateResourceID("dataCenter", req.DataCenter); err != nil {
		return nil, err
	}

	if req.Type != "" && req.Type != "STANDARD" && req.Type != "HIGH_PERFORMANCE" {
		return nil, NewValidationError("type", "must be STANDARD or HIGH_PERFORMANCE")
	}
	var volume NetworkVolume
	if err := c.Post(ctx, "/network-volumes", req, &volume); err != nil {
		return nil, fmt.Errorf("failed to create network volume: %w", err)
	}
	if err := validateNetworkVolume(volume, ""); err != nil {
		return nil, err
	}
	if volume.Name != req.Name || volume.Size != req.Size || volume.DataCenter != req.DataCenter || (req.Type != "" && volume.Type != req.Type) {
		return nil, fmt.Errorf("created network volume disagrees with request")
	}
	return &volume, nil
}

// UpdateNetworkVolume updates a network volume's name and/or size (resize
// can only grow — RunPod rejects shrinks).
func (c *Client) UpdateNetworkVolume(ctx context.Context, volumeID string, req *UpdateNetworkVolumeRequest) (*NetworkVolume, error) {
	if err := validateResourceID("volumeID", volumeID); err != nil {
		return nil, err
	}
	if req == nil || (req.Name == "" && req.Size == 0) {
		return nil, NewValidationError("request", "must set name and/or size")
	}
	if req.Size != 0 && (req.Size < 10 || req.Size > 4096) {
		return nil, NewValidationError("size", "must be between 10 and 4096 GB")
	}

	var volume NetworkVolume
	if err := c.Patch(ctx, "/network-volumes/"+url.PathEscape(volumeID), req, &volume); err != nil {
		return nil, fmt.Errorf("failed to update network volume %s: %w", volumeID, err)
	}
	if err := validateNetworkVolume(volume, volumeID); err != nil {
		return nil, err
	}
	if (req.Name != "" && volume.Name != req.Name) || (req.Size != 0 && volume.Size != req.Size) {
		return nil, fmt.Errorf("updated network volume disagrees with request")
	}
	return &volume, nil
}

// DeleteNetworkVolume deletes a network volume by ID.
func (c *Client) DeleteNetworkVolume(ctx context.Context, volumeID string) error {
	if err := validateResourceID("volumeID", volumeID); err != nil {
		return err
	}
	if err := c.Delete(ctx, "/network-volumes/"+url.PathEscape(volumeID)); err != nil {
		return fmt.Errorf("failed to delete network volume %s: %w", volumeID, err)
	}
	return nil
}

func (c *Client) listNetworkVolumes(ctx context.Context, read catalogReader) ([]NetworkVolume, error) {
	volumes, err := paginatedListRead[NetworkVolume](ctx, read, "/network-volumes", "networkVolumes")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, volume := range volumes {
		if err := validateNetworkVolume(volume, ""); err != nil {
			return nil, err
		}
		if seen[volume.ID] {
			return nil, fmt.Errorf("network volume list repeated id %q", volume.ID)
		}
		seen[volume.ID] = true
	}
	return volumes, nil
}

func validateNetworkVolume(volume NetworkVolume, requestedID string) error {
	if err := validateResourceIdentity("network volume", volume.ID, volume.Name, requestedID); err != nil {
		return err
	}
	if err := validateResourceID("dataCenter", volume.DataCenter); err != nil {
		return fmt.Errorf("network volume %s omitted a valid dataCenter", volume.ID)
	}
	if volume.Size < 10 || volume.Size > 4096 {
		return fmt.Errorf("network volume %s has invalid size %d", volume.ID, volume.Size)
	}
	if volume.Type != "STANDARD" && volume.Type != "HIGH_PERFORMANCE" {
		return fmt.Errorf("network volume %s has missing or unknown type %q", volume.ID, volume.Type)
	}
	return nil
}
