package runpod

import (
	"context"
	"fmt"
)

// CreateSecret creates a new secret
func (c *Client) CreateSecret(ctx context.Context, req *CreateSecretRequest) (*Secret, error) {
	if err := c.validateCreateSecretRequest(req); err != nil {
		return nil, err
	}

	var secret Secret
	err := c.Post(ctx, "/secrets", req, &secret)
	if err != nil {
		return nil, fmt.Errorf("failed to create secret: %w", err)
	}

	return &secret, nil
}

// GetSecret retrieves a secret by name (value not included for security)
func (c *Client) GetSecret(ctx context.Context, name string) (*Secret, error) {
	if err := c.validateRequired("name", name); err != nil {
		return nil, err
	}

	var secret Secret
	endpoint := fmt.Sprintf("/secrets/%s", name)
	err := c.Get(ctx, endpoint, &secret)
	if err != nil {
		return nil, fmt.Errorf("failed to get secret %s: %w", name, err)
	}

	return &secret, nil
}

// UpdateSecret updates an existing secret
func (c *Client) UpdateSecret(ctx context.Context, name string, req *UpdateSecretRequest) (*Secret, error) {
	if err := c.validateRequired("name", name); err != nil {
		return nil, err
	}
	if err := c.validateRequired("value", req.Value); err != nil {
		return nil, err
	}

	var secret Secret
	endpoint := fmt.Sprintf("/secrets/%s", name)
	err := c.Put(ctx, endpoint, req, &secret)
	if err != nil {
		return nil, fmt.Errorf("failed to update secret %s: %w", name, err)
	}

	return &secret, nil
}

// CreateOrUpdateSecret creates a secret if it doesn't exist, or updates it if it does
func (c *Client) CreateOrUpdateSecret(ctx context.Context, name, value string) error {
	// Try to get existing secret first
	_, err := c.GetSecret(ctx, name)
	if err != nil {
		// If not found, create new secret
		if apiErr, ok := err.(*APIError); ok && apiErr.IsNotFound() {
			_, createErr := c.CreateSecret(ctx, &CreateSecretRequest{
				Name:  name,
				Value: value,
			})
			return createErr
		}
		return err
	}

	// Secret exists, update it
	_, updateErr := c.UpdateSecret(ctx, name, &UpdateSecretRequest{
		Value: value,
	})
	return updateErr
}

// DeleteSecret deletes a secret
func (c *Client) DeleteSecret(ctx context.Context, name string) error {
	if err := c.validateRequired("name", name); err != nil {
		return err
	}

	endpoint := fmt.Sprintf("/secrets/%s", name)
	err := c.Delete(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("failed to delete secret %s: %w", name, err)
	}

	return nil
}

// ListSecrets lists all secrets (values not included)
func (c *Client) ListSecrets(ctx context.Context, opts *ListOptions) ([]*Secret, error) {
	endpoint := c.buildListURL("/secrets", opts)

	var response struct {
		Secrets []*Secret `json:"secrets"`
	}

	err := c.Get(ctx, endpoint, &response)
	if err != nil {
		return nil, fmt.Errorf("failed to list secrets: %w", err)
	}

	return response.Secrets, nil
}

// validateCreateSecretRequest validates a secret creation request
func (c *Client) validateCreateSecretRequest(req *CreateSecretRequest) error {
	if req == nil {
		return NewValidationError("request", "cannot be nil")
	}

	if err := c.validateRequired("name", req.Name); err != nil {
		return err
	}
	if err := c.validateRequired("value", req.Value); err != nil {
		return err
	}

	return nil
}
