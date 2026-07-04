package runpod

import (
	"context"
	"fmt"
	"time"
)

// =========================
// SERVERLESS JOB OPERATIONS
// =========================

// RunAsync submits an asynchronous job to a serverless endpoint
// Returns immediately with a job ID for later status checking
func (c *Client) RunAsync(ctx context.Context, endpointID string, input interface{}) (*Job, error) {
	if err := c.validateRequired("endpointID", endpointID); err != nil {
		return nil, err
	}

	req := &RunJobRequest{Input: input}

	endpoint := c.serverlessURL(fmt.Sprintf("/v2/%s/run", endpointID))

	var job Job
	err := c.Post(ctx, endpoint, req, &job)
	if err != nil {
		return nil, fmt.Errorf("failed to submit async job to endpoint %s: %w", endpointID, err)
	}

	return &job, nil
}

// RunSync submits a synchronous job and waits for completion.
// Blocks until the job completes or times out.
//
// The client's HTTP timeout (default 30s) bounds this call. RunPod holds
// /runsync connections for up to ~90s for long jobs — construct the client
// with WithTimeout (or use RunAsync + WaitForJobCompletion) for jobs that
// may run longer than the configured timeout.
func (c *Client) RunSync(ctx context.Context, endpointID string, input interface{}) (*Job, error) {
	if err := c.validateRequired("endpointID", endpointID); err != nil {
		return nil, err
	}

	req := &RunJobRequest{Input: input}

	endpoint := c.serverlessURL(fmt.Sprintf("/v2/%s/runsync", endpointID))

	var job Job
	err := c.Post(ctx, endpoint, req, &job)
	if err != nil {
		return nil, fmt.Errorf("failed to submit sync job to endpoint %s: %w", endpointID, err)
	}

	return &job, nil
}

// GetJobStatus retrieves the status and results of a job
func (c *Client) GetJobStatus(ctx context.Context, endpointID, jobID string) (*Job, error) {
	if err := c.validateRequired("endpointID", endpointID); err != nil {
		return nil, err
	}
	if err := c.validateRequired("jobID", jobID); err != nil {
		return nil, err
	}

	endpoint := c.serverlessURL(fmt.Sprintf("/v2/%s/status/%s", endpointID, jobID))

	var job Job
	err := c.Get(ctx, endpoint, &job)
	if err != nil {
		return nil, fmt.Errorf("failed to get status for job %s on endpoint %s: %w", jobID, endpointID, err)
	}

	return &job, nil
}

// CancelJob cancels a running or queued job
func (c *Client) CancelJob(ctx context.Context, endpointID, jobID string) error {
	if err := c.validateRequired("endpointID", endpointID); err != nil {
		return err
	}
	if err := c.validateRequired("jobID", jobID); err != nil {
		return err
	}

	endpoint := c.serverlessURL(fmt.Sprintf("/v2/%s/cancel/%s", endpointID, jobID))

	err := c.Post(ctx, endpoint, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to cancel job %s on endpoint %s: %w", jobID, endpointID, err)
	}

	return nil
}

// RetryJob retries a failed or timed-out job using the same job ID and input
func (c *Client) RetryJob(ctx context.Context, endpointID, jobID string) (*Job, error) {
	if err := c.validateRequired("endpointID", endpointID); err != nil {
		return nil, err
	}
	if err := c.validateRequired("jobID", jobID); err != nil {
		return nil, err
	}

	endpoint := c.serverlessURL(fmt.Sprintf("/v2/%s/retry/%s", endpointID, jobID))

	var job Job
	err := c.Post(ctx, endpoint, nil, &job)
	if err != nil {
		return nil, fmt.Errorf("failed to retry job %s on endpoint %s: %w", jobID, endpointID, err)
	}

	return &job, nil
}

// PurgeQueue clears all pending jobs from the endpoint queue
// Does not affect jobs that are already running
func (c *Client) PurgeQueue(ctx context.Context, endpointID string) error {
	if err := c.validateRequired("endpointID", endpointID); err != nil {
		return err
	}

	endpoint := c.serverlessURL(fmt.Sprintf("/v2/%s/purge-queue", endpointID))

	err := c.Post(ctx, endpoint, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to purge queue for endpoint %s: %w", endpointID, err)
	}

	return nil
}

// GetHealth checks the operational status of an endpoint
func (c *Client) GetHealth(ctx context.Context, endpointID string) (*EndpointHealth, error) {
	if err := c.validateRequired("endpointID", endpointID); err != nil {
		return nil, err
	}

	endpoint := c.serverlessURL(fmt.Sprintf("/v2/%s/health", endpointID))

	var health EndpointHealth
	err := c.Get(ctx, endpoint, &health)
	if err != nil {
		return nil, fmt.Errorf("failed to get health for endpoint %s: %w", endpointID, err)
	}

	return &health, nil
}

// ================================
// JOB MONITORING AND UTILITIES
// ================================

// WaitForJobCompletion waits for a job to complete or fail
// Returns the final job state or an error if timeout is reached
func (c *Client) WaitForJobCompletion(ctx context.Context, endpointID, jobID string, maxWaitTime time.Duration) (*Job, error) {
	if maxWaitTime <= 0 {
		maxWaitTime = 10 * time.Minute // Default timeout
	}

	deadline := time.Now().Add(maxWaitTime)

	for time.Now().Before(deadline) {
		job, err := c.GetJobStatus(ctx, endpointID, jobID)
		if err != nil {
			return nil, err
		}

		// Check if job is in a terminal state
		switch JobStatus(job.Status) {
		case JobStatusCompleted:
			return job, nil
		case JobStatusFailed:
			return job, fmt.Errorf("job %s failed: %s", jobID, job.Error)
		case JobStatusCancelled:
			return job, fmt.Errorf("job %s was cancelled", jobID)
		case JobStatusTimedOut:
			return job, fmt.Errorf("job %s timed out", jobID)
		}

		// Wait before next check
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			// Continue polling
		}
	}

	return nil, fmt.Errorf("job %s did not complete within %v", jobID, maxWaitTime)
}

// IsJobTerminal checks if a job is in a terminal state (completed, failed, etc.)
func (c *Client) IsJobTerminal(status string) bool {
	terminalStates := []JobStatus{
		JobStatusCompleted,
		JobStatusFailed,
		JobStatusCancelled,
		JobStatusTimedOut,
	}

	jobStatus := JobStatus(status)
	for _, terminalStatus := range terminalStates {
		if jobStatus == terminalStatus {
			return true
		}
	}

	return false
}

// ================================
// STREAMING SUPPORT
// ================================

// StreamResults retrieves partial/streaming results from a job
// This is useful for jobs that generate output incrementally (like text generation)
// or have very large outputs that benefit from chunked delivery
func (c *Client) StreamResults(ctx context.Context, endpointID, jobID string) (*Job, error) {
	if err := c.validateRequired("endpointID", endpointID); err != nil {
		return nil, err
	}
	if err := c.validateRequired("jobID", jobID); err != nil {
		return nil, err
	}

	// RunPod stream endpoint: /v2/{endpoint_id}/stream/{job_id}
	endpoint := c.serverlessURL(fmt.Sprintf("/v2/%s/stream/%s", endpointID, jobID))

	var job Job
	err := c.Get(ctx, endpoint, &job)
	if err != nil {
		return nil, fmt.Errorf("failed to stream results for job %s on endpoint %s: %w", jobID, endpointID, err)
	}

	return &job, nil
}

// ================================
// CONVENIENCE FUNCTIONS
// ================================

// RunAndWait submits a job asynchronously and waits for completion
// Combines RunAsync + WaitForJobCompletion for convenience
func (c *Client) RunAndWait(ctx context.Context, endpointID string, input interface{}, maxWaitTime time.Duration) (*Job, error) {
	// Submit job
	job, err := c.RunAsync(ctx, endpointID, input)
	if err != nil {
		return nil, err
	}

	// Wait for completion
	return c.WaitForJobCompletion(ctx, endpointID, job.ID, maxWaitTime)
}
