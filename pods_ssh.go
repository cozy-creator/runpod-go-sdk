package runpod

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// SSHTarget describes how to reach a pod over SSH for diagnostic access.
//
// SECURITY: SSH access into a pod is intended ONLY as a development /
// diagnostic fallback. Production workers must NOT be created with SSH
// enabled — an attacker with the pod's public key (or one stolen from CI)
// could exfiltrate model weights and cached credentials, or compromise the
// worker process. Callers gate SSH-key injection at create time; these
// helpers only consume what the pod exposes if SSH is already configured.
type SSHTarget struct {
	Host string
	Port int
	User string
}

type podRuntimePortGQL struct {
	IP          string `json:"ip"`
	IsIPPublic  bool   `json:"isIpPublic"`
	PrivatePort int    `json:"privatePort"`
	PublicPort  int    `json:"publicPort"`
	Type        string `json:"type"`
}

type podRuntimeForSSHGQL struct {
	Ports []podRuntimePortGQL `json:"ports"`
}

type podForSSHGQL struct {
	ID      string               `json:"id"`
	Runtime *podRuntimeForSSHGQL `json:"runtime"`
}

type sshDiscoveryResp struct {
	Pod *podForSSHGQL `json:"pod"`
}

// DiscoverSSHTarget reads the pod's runtime port mappings and returns the
// publicly-routable host:port that maps to in-pod port 22.
//
// Returns an error if:
//   - The pod isn't far enough along to have a runtime block (still pulling
//     image or starting container).
//   - The pod was created without SSH enabled (no privatePort=22 mapping).
//
// The pod must have been created with `PUBLIC_KEY=<your-pubkey>` in its env
// for runpod's image-runtime to install the key into authorized_keys. Without
// that, the pod will be reachable on the discovered port but auth will fail.
func (c *Client) DiscoverSSHTarget(ctx context.Context, podID string) (*SSHTarget, error) {
	if err := c.validateRequired("podID", podID); err != nil {
		return nil, err
	}

	query := `query($input: PodFilter!) {
  pod(input: $input) {
    id
    runtime {
      ports {
        ip
        isIpPublic
        privatePort
        publicPort
        type
      }
    }
  }
}`
	variables := map[string]interface{}{
		"input": map[string]interface{}{"podId": podID},
	}

	var resp sshDiscoveryResp
	if err := c.GraphQL(ctx, query, variables, &resp); err != nil {
		return nil, fmt.Errorf("ssh-discover %s: %w", podID, err)
	}
	if resp.Pod == nil {
		return nil, fmt.Errorf("ssh-discover %s: pod not found", podID)
	}
	if resp.Pod.Runtime == nil {
		return nil, fmt.Errorf("ssh-discover %s: runtime not yet up (still pulling image or starting?)", podID)
	}

	for _, p := range resp.Pod.Runtime.Ports {
		if p.PrivatePort != 22 {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(p.Type), "tcp") {
			continue
		}
		host := strings.TrimSpace(p.IP)
		port := p.PublicPort
		if host == "" || port == 0 {
			continue
		}
		return &SSHTarget{Host: host, Port: port, User: "root"}, nil
	}

	// Surface the available mappings so callers can spot a misconfigured pod.
	available, _ := json.Marshal(resp.Pod.Runtime.Ports)
	return nil, fmt.Errorf("ssh-discover %s: no privatePort=22/tcp mapping (was pod created with PUBLIC_KEY env?). available ports=%s", podID, string(available))
}

// StreamPodCommandOptions configures how StreamPodCommand invokes ssh.
type StreamPodCommandOptions struct {
	// IdentityFile — path to the private key matching the PUBLIC_KEY the pod
	// was created with. When unset, ssh uses the agent / default identities.
	IdentityFile string
	// ConnectTimeout — TCP connect timeout passed to ssh's -o ConnectTimeout.
	// Default 10 seconds.
	ConnectTimeout time.Duration
	// ExtraSSHArgs — appended verbatim before the destination. Use sparingly.
	ExtraSSHArgs []string
}

// StreamPodCommand executes a shell command on the pod over SSH and streams
// stdout+stderr into the supplied writer. Returns when the remote command
// exits or ctx is cancelled.
//
// SECURITY: Diagnostic-only. Do not call this from production code paths.
// gen-orchestrator's autoscale post-mortem path is the only intended caller,
// and only when the operator has explicitly opted into SSH diagnostics by
// configuring RUNPOD_SSH_PUBLIC_KEY in a non-production environment.
//
// Implementation note: this shells out to the system `ssh` binary rather
// than using golang.org/x/crypto/ssh because diagnostic access only happens
// from operator-controlled environments (CI, dev workstations) where ssh is
// already installed and configured. Avoids pulling a crypto dependency into
// the SDK for a fallback feature.
func (c *Client) StreamPodCommand(ctx context.Context, podID, cmd string, stdout io.Writer, opts *StreamPodCommandOptions) error {
	if err := c.validateRequired("podID", podID); err != nil {
		return err
	}
	if strings.TrimSpace(cmd) == "" {
		return NewValidationError("cmd", "cannot be empty")
	}
	if stdout == nil {
		return NewValidationError("stdout", "cannot be nil")
	}

	target, err := c.DiscoverSSHTarget(ctx, podID)
	if err != nil {
		return err
	}

	connectTimeout := 10 * time.Second
	if opts != nil && opts.ConnectTimeout > 0 {
		connectTimeout = opts.ConnectTimeout
	}

	args := []string{
		"-p", strconv.Itoa(target.Port),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes",
		"-o", fmt.Sprintf("ConnectTimeout=%d", int(connectTimeout.Seconds())),
		"-o", "LogLevel=ERROR",
	}
	if opts != nil && strings.TrimSpace(opts.IdentityFile) != "" {
		args = append(args, "-i", opts.IdentityFile, "-o", "IdentitiesOnly=yes")
	}
	if opts != nil {
		for _, extra := range opts.ExtraSSHArgs {
			if strings.TrimSpace(extra) != "" {
				args = append(args, extra)
			}
		}
	}
	args = append(args, fmt.Sprintf("%s@%s", target.User, target.Host), cmd)

	sshCmd := exec.CommandContext(ctx, "ssh", args...)
	sshCmd.Stdout = stdout
	sshCmd.Stderr = stdout

	if c.debug {
		c.logger.Printf("[DEBUG] StreamPodCommand pod=%s cmd=%q ssh-args=%v", podID, cmd, args)
	}

	if err := sshCmd.Run(); err != nil {
		return fmt.Errorf("ssh into pod %s failed: %w", podID, err)
	}
	return nil
}
