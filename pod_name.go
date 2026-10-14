package runpod

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// RenamePod updates only a pod's console label through REST v2. Omitted
// container settings stay untouched; this does not stop or reset the pod.
// See https://docs.runpod.io/api-reference-v2/pods/update-a-pod.
func (c *Client) RenamePod(ctx context.Context, podID, name string) error {
	if err := c.validateRequired("podID", strings.TrimSpace(podID)); err != nil {
		return err
	}
	if err := c.validateRequired("name", strings.TrimSpace(name)); err != nil {
		return err
	}
	var renamed struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	endpoint := strings.TrimRight(c.restV2BaseURL, "/") + "/pods/" + url.PathEscape(podID)
	if err := c.Patch(ctx, endpoint, map[string]string{"name": name}, &renamed); err != nil {
		return err
	}
	if renamed.ID != podID || renamed.Name != name {
		return fmt.Errorf("rename pod: response did not confirm pod %q name %q", podID, name)
	}
	return nil
}
