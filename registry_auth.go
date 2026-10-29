package runpod

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ListContainerRegistryAuths lists stored container registry credentials.
func (c *Client) ListContainerRegistryAuths(ctx context.Context) ([]ContainerRegistryAuth, error) {
	rows, err := decodeList[ContainerRegistryAuth](ctx, c, "/registries", "registries")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if err := validateResourceIdentity("registry", row.ID, row.Name, ""); err != nil {
			return nil, err
		}
		if seen[row.ID] {
			return nil, fmt.Errorf("registry list repeated id %q", row.ID)
		}
		seen[row.ID] = true
	}
	return rows, nil
}

// CreateContainerRegistryAuth stores a Docker registry credential. Names must
// be unique per account.
func (c *Client) CreateContainerRegistryAuth(ctx context.Context, req *CreateContainerRegistryAuthRequest) (*ContainerRegistryAuth, error) {
	if req == nil {
		return nil, NewValidationError("request", "cannot be nil")
	}
	if err := c.validateRequired("name", req.Name); err != nil {
		return nil, err
	}
	if err := c.validateRequired("username", req.Username); err != nil {
		return nil, err
	}
	if err := c.validateRequired("password", req.Password); err != nil {
		return nil, err
	}

	var auth ContainerRegistryAuth
	if err := c.Post(ctx, "/registries", req, &auth); err != nil {
		return nil, fmt.Errorf("failed to create container registry auth: %w", err)
	}
	if err := validateResourceIdentity("registry", auth.ID, auth.Name, ""); err != nil {
		return nil, err
	}
	if auth.Name != req.Name {
		return nil, fmt.Errorf("created registry name disagrees with request")
	}
	return &auth, nil
}

// GetContainerRegistryAuth reads a stored credential's metadata by ID.
// Usernames and passwords are write-only and are not returned.
func (c *Client) GetContainerRegistryAuth(ctx context.Context, authID string) (*ContainerRegistryAuth, error) {
	if err := validateResourceID("authID", authID); err != nil {
		return nil, err
	}
	var auth ContainerRegistryAuth
	if err := c.Get(ctx, "/registries/"+url.PathEscape(authID), &auth); err != nil {
		return nil, fmt.Errorf("get registry: %w", err)
	}
	if err := validateResourceIdentity("registry", auth.ID, auth.Name, authID); err != nil {
		return nil, err
	}
	return &auth, nil
}

// DeleteContainerRegistryAuth deletes a stored registry credential by ID.
func (c *Client) DeleteContainerRegistryAuth(ctx context.Context, authID string) error {
	if err := validateResourceID("authID", authID); err != nil {
		return err
	}
	if err := c.Delete(ctx, "/registries/"+url.PathEscape(authID)); err != nil {
		return fmt.Errorf("failed to delete container registry auth %s: %w", authID, err)
	}
	return nil
}

// registryAuthMessages are substrings that indicate a pod-create failure was
// caused by a bad/stale registry credential or an unpullable private
// image. RunPod does not surface structured error codes for these, so fuzzy
// matching is the only available signal.
var registryAuthMessages = []string{
	"registry auth",
	"registry_auth",
	"registryauth",
	"container registry",
	"containerregistry",
	"invalid auth",
	"no such auth",
	"auth id",
	"auth_id",
	"authentication failed",
	"pull access denied",
	"access denied",
	"unauthorized",
}

// IsRegistryAuthError reports whether a pod-create error looks like it was
// caused by bad/stale container registry credentials rather than capacity or
// request problems. SDK-level auth errors (bad RunPod API key) return false.
func IsRegistryAuthError(err error) bool {
	if err == nil {
		return false
	}
	// The SDK's own API-key/permission failures (401/403) are not registry
	// auth problems — those come back as pod-create 5xxs with auth-ish text.
	if errors.Is(err, ErrUnauthorized) {
		return false
	}
	// Capacity failures are never registry-auth failures.
	if errors.Is(err, ErrNoCapacity) {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, m := range registryAuthMessages {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}
