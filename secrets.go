package runpod

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var secretNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.\-/]*$`)

func validateSecretName(name string) error {
	if len(name) > 191 || !secretNamePattern.MatchString(name) || strings.HasPrefix(strings.ToUpper(name), "RUNPOD") {
		return NewValidationError("name", "must be a valid non-reserved secret name of at most 191 characters")
	}
	return nil
}
func validateSecretValue(value string) error {
	if value == "" || len(value) >= 16777216 || !utf8.ValidString(value) {
		return NewValidationError("value", "must be non-empty UTF-8 text smaller than 16 MiB")
	}
	return nil
}
func validateSecretDescription(description string) error {
	if len(description) > 65535 || !utf8.ValidString(description) {
		return NewValidationError("description", "must be UTF-8 text of at most 65535 bytes")
	}
	return nil
}
func validateSecret(secret *Secret, requestedID string) error {
	if secret == nil {
		return fmt.Errorf("secret response contained a null row")
	}
	return validateResourceIdentity("secret", secret.ID, secret.Name, requestedID)
}
func (c *Client) CreateSecret(ctx context.Context, req *CreateSecretRequest) (*Secret, error) {
	if req == nil {
		return nil, NewValidationError("request", "cannot be nil")
	}
	if err := validateSecretName(req.Name); err != nil {
		return nil, err
	}
	if err := validateSecretValue(req.Value); err != nil {
		return nil, err
	}
	if err := validateSecretDescription(req.Description); err != nil {
		return nil, err
	}
	var secret Secret
	if err := c.Post(ctx, "/account/secrets", req, &secret); err != nil {
		return nil, fmt.Errorf("create secret: %w", err)
	}
	if err := validateSecret(&secret, ""); err != nil {
		return nil, err
	}
	if !strings.EqualFold(secret.Name, req.Name) {
		return nil, fmt.Errorf("created secret name disagrees with request")
	}
	return &secret, nil
}

// GetSecret uses the immutable secret ID, not the name.
func (c *Client) GetSecret(ctx context.Context, id string) (*Secret, error) {
	if err := validateResourceID("id", id); err != nil {
		return nil, err
	}
	var secret Secret
	if err := c.Get(ctx, "/account/secrets/"+url.PathEscape(id), &secret); err != nil {
		return nil, fmt.Errorf("get secret: %w", err)
	}
	if err := validateSecret(&secret, id); err != nil {
		return nil, err
	}
	return &secret, nil
}

// UpdateSecret updates only provided fields. Value and description changes are
// not atomic at the provider; an error can follow a successful value rotation.
func (c *Client) UpdateSecret(ctx context.Context, id string, req *UpdateSecretRequest) (*Secret, error) {
	if err := validateResourceID("id", id); err != nil {
		return nil, err
	}
	if req == nil || (req.Value == "" && req.Description == nil) {
		return nil, NewValidationError("request", "must set value and/or description")
	}
	if req.Value != "" {
		if err := validateSecretValue(req.Value); err != nil {
			return nil, err
		}
	}
	if req.Description != nil {
		if err := validateSecretDescription(*req.Description); err != nil {
			return nil, err
		}
	}
	var secret Secret
	if err := c.Patch(ctx, "/account/secrets/"+url.PathEscape(id), req, &secret); err != nil {
		return nil, fmt.Errorf("update secret: %w", err)
	}
	if err := validateSecret(&secret, id); err != nil {
		return nil, err
	}
	return &secret, nil
}
func (c *Client) DeleteSecret(ctx context.Context, id string) error {
	if err := validateResourceID("id", id); err != nil {
		return err
	}
	return c.Delete(ctx, "/account/secrets/"+url.PathEscape(id))
}
func (c *Client) listSecrets(ctx context.Context, endpoint string) ([]*Secret, error) {
	rows, err := decodeList[*Secret](ctx, c, endpoint, "secrets")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, secret := range rows {
		if err := validateSecret(secret, ""); err != nil {
			return nil, err
		}
		if seen[secret.ID] {
			return nil, fmt.Errorf("secret list repeated id %q", secret.ID)
		}
		seen[secret.ID] = true
	}
	return rows, nil
}
func (c *Client) ListSecrets(ctx context.Context) ([]*Secret, error) {
	return c.listSecrets(ctx, "/account/secrets")
}

// CreateOrUpdateSecret resolves a name with the case-insensitive v2 name
// filter, then patches by ID. Only a successful explicit empty list authorizes
// creation; authorization, transport and missing-route errors remain errors.
func (c *Client) CreateOrUpdateSecret(ctx context.Context, name, value string) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	if err := validateSecretValue(value); err != nil {
		return err
	}
	rows, err := c.listSecrets(ctx, "/account/secrets?name="+url.QueryEscape(name))
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		if len(rows) != 1 || !strings.EqualFold(rows[0].Name, name) {
			return fmt.Errorf("secret name lookup returned an ambiguous result")
		}
		_, err = c.UpdateSecret(ctx, rows[0].ID, &UpdateSecretRequest{Value: value})
		return err
	}
	_, err = c.CreateSecret(ctx, &CreateSecretRequest{Name: name, Value: value})
	return err
}
