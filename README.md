# runpod-go-sdk

Go client for the RunPod API: pods, serverless jobs, GPU catalog queries, network volumes, container registry auths, and secrets.

## Install

```bash
go get github.com/cozy-creator/runpod-go-sdk
```

## Quick start

```go
client, err := runpod.NewClient(os.Getenv("RUNPOD_API_KEY"))
if err != nil {
    log.Fatal(err)
}

pod, err := client.CreatePod(ctx, &runpod.CreatePodRequest{
    Name:              "worker-1",
    ImageName:         "runpod/pytorch:2.1.0-py3.10-cuda11.8.0",
    GPUTypeIDs:        []string{"NVIDIA GeForce RTX 4090", "NVIDIA GeForce RTX 3090"},
    GPUCount:          1,
    ContainerDiskInGB: 50,
    CloudType:         "COMMUNITY",
})
```

### Client options

```go
client, err := runpod.NewClient(apiKey,
    runpod.WithTimeout(120*time.Second),      // HTTP timeout (default 30s)
    runpod.WithMaxRetryAttempts(5),           // retries with exponential backoff + jitter
    runpod.WithRetryDelay(2*time.Second),     // backoff base delay
    runpod.WithDebug(true),                   // request/response logging
    runpod.WithLogger(customLogger),
    runpod.WithHTTPClient(customHTTPClient),
    runpod.WithBaseURL(...),                  // REST base (default https://rest.runpod.io/v1)
    runpod.WithServerlessBaseURL(...),        // serverless base (default https://api.runpod.ai)
    runpod.WithGraphQLBaseURL(...),           // GraphQL base (default https://api.runpod.io/graphql)
)
```

Retries: GETs and other idempotent requests retry on 5xx/429 with exponential backoff and jitter, honoring `Retry-After` on 429. POSTs never retry on 5xx (not idempotent, and RunPod uses 500 for ordinary stock-outs).

## Pods

| Function | Description |
|----------|-------------|
| `CreatePod` | Create a pod. With multiple `GPUTypeIDs`, fans out per type (see below) |
| `CreatePodWithFallback` | Explicit per-GPU-type fan-out with filter/failure hooks |
| `CreateSpotPod` | Create an interruptible (spot) pod |
| `GetPod` / `GetPodWithOptions` | Fetch a pod (optional `includeMachine`, `includeNetworkVolume`, ...) |
| `ListPods` | List pods with pagination |
| `StopPod` / `ResumePod` / `TerminatePod` | Lifecycle |
| `WaitForPodReady` | Poll until runtime is up; returns startup-timing decomposition |
| `PodTimingSnapshot` | One-shot timing decomposition |
| `GetPodDiagnostics` | Normalized diagnostics snapshot (runtime readiness, datacenter, reason) |
| `GetProviderFeatureSupport` | Provider capability flags (pod logs: unsupported by RunPod's public API) |

### Stock-outs and GPU-type fallback

RunPod's REST `POST /pods` does **not** walk a multi-entry `gpuTypeIds` list when the first type has no stock — it returns 500 "no instances available". The SDK owns that protocol quirk:

```go
pod, err := client.CreatePod(ctx, req) // multiple GPUTypeIDs → one request per type
if errors.Is(err, runpod.ErrNoCapacity) {
    // every candidate was out of stock
    var exhausted *runpod.FallbackExhaustedError
    if errors.As(err, &exhausted) {
        for _, a := range exhausted.Attempts { ... }
    }
}
```

`CreatePodWithFallback` adds a `CandidateFilter` (skip recently-failed types) and an `OnAttemptFailure` hook for consumer-side failure tracking.

### Pod logs

RunPod's public REST/GraphQL APIs expose no pod-log endpoint. The SDK deliberately has no log-fetching surface; use `GetPodDiagnostics` for status/runtime/machine troubleshooting.

## GPU catalog (GraphQL)

```go
gpus, err := client.ListGPUTypes(ctx, &runpod.GPUTypeFilter{MinCudaVersion: "12.8"})
available, err := client.ListAvailableGPUs(ctx, "12.8", 1) // in-stock, sorted by price
gpu, err := client.GetGPUType(ctx, "NVIDIA GeForce RTX 4090")
```

`ListAvailableGPUs` returns `StockStatus` and `LowestPrice` (bid/on-demand) per type.

## Serverless jobs

| Function | Description |
|----------|-------------|
| `RunAsync` / `RunSync` | Submit a job (async returns immediately; sync blocks) |
| `GetJobStatus` | Job status + results |
| `WaitForJobCompletion` | Poll until terminal |
| `RunAndWait` | RunAsync + WaitForJobCompletion |
| `StreamResults` | Fetch partial/streaming results once |
| `CancelJob` / `RetryJob` / `PurgeQueue` | Queue management |
| `GetHealth` | Endpoint health |
| `IsJobTerminal` | Terminal-status check |

`Job.Input` / `Job.Output` / `Job.Stream` are `json.RawMessage` — unmarshal into your own types:

```go
job, err := client.RunSync(ctx, endpointID, map[string]any{"prompt": "hello"})
var out MyOutput
json.Unmarshal(job.Output, &out)
```

`RunSync` is bounded by the client HTTP timeout (default 30s); RunPod holds `/runsync` up to ~90s. Use `WithTimeout` or `RunAsync`+`WaitForJobCompletion` for longer jobs.

## Network volumes and registry auths (REST)

```go
vols, err := client.ListNetworkVolumes(ctx)
vol, err := client.CreateNetworkVolume(ctx, &runpod.CreateNetworkVolumeRequest{
    Name: "models", Size: 100, DataCenterID: "EU-RO-1",
})
vol, err = client.UpdateNetworkVolume(ctx, vol.ID, &runpod.UpdateNetworkVolumeRequest{Size: 200}) // grow-only
err = client.DeleteNetworkVolume(ctx, vol.ID)

auth, err := client.CreateContainerRegistryAuth(ctx, &runpod.CreateContainerRegistryAuthRequest{
    Name: "my-registry", Username: "bob", Password: token,
})
auths, err := client.ListContainerRegistryAuths(ctx)
err = client.DeleteContainerRegistryAuth(ctx, auth.ID)
```

`IsRegistryAuthError(err)` classifies pod-create failures caused by bad/stale registry credentials.

Secrets: `CreateSecret` / `GetSecret` / `UpdateSecret` / `CreateOrUpdateSecret` / `DeleteSecret` / `ListSecrets`.

## Error handling

Two error types plus sentinels:

- `*runpod.APIError` — HTTP errors; carries `StatusCode`, `Message`, `RetryAfter` (on 429)
- `*runpod.ValidationError` — client-side input validation
- `*runpod.NoCapacityError` / `*runpod.FallbackExhaustedError` — pod-create stock-outs

Match with `errors.Is` / `errors.As`:

```go
switch {
case errors.Is(err, runpod.ErrNotFound):
case errors.Is(err, runpod.ErrUnauthorized):
case errors.Is(err, runpod.ErrRateLimited):
case errors.Is(err, runpod.ErrNoCapacity):
}
```

## Testing

```bash
go test ./...                          # unit tests (mock servers)
RUNPOD_API_KEY=... go test ./...       # additionally runs live API tests
```

## License

MIT
