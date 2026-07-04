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

### Static SKU catalog

The SDK owns a static `GPUSpec` catalog (type ID, VRAM, SM compute capability, consumer flag) covering Ampere through Blackwell (RTX 5090, B200, RTX PRO 6000 Blackwell, H200, ...), ordered by fallback preference:

```go
specs := runpod.GPUsWithAtLeast(24, 89) // >=24GB VRAM, SM >= 8.9
req.GPUTypeIDs = runpod.GPUTypeIDs(specs) // directly usable as a fallback chain
spec, ok := runpod.GPUSpecByID("NVIDIA GeForce RTX 4090")
```

Static data is verified against the live `gpuTypes` query by a live-gated test; refresh stock/pricing at runtime with `ListAvailableGPUs` / `ListGPUOffers`.

### Offers and spot (interruptible) pods

`ListGPUOffers` returns per-(GPU type x cloud) stock and pricing in one call, sorted by on-demand price — the placement view that connects the catalog to pod creation:

```go
offers, _ := client.ListGPUOffers(ctx, &runpod.GPUOfferFilter{MinCudaVersion: "12.8", InStockOnly: true})
offer := offers[0] // cheapest in stock
pod, err := client.CreateSpotPod(ctx, &runpod.CreatePodRequest{
    // ...
    GPUTypeIDs: []string{offer.GPUTypeID},
    CloudType:  offer.CloudType,
    BidPerGPU:  offer.MinimumBidPrice, // USD/hr per GPU; omit to bid the floor
})
```

Reclaim: a preempted spot pod is stopped, not deleted — it reports `desiredStatus="EXITED"` with the runtime cleared. There is no dedicated preemption signal in the public API; treat an unexpected EXITED on an interruptible pod as a probable reclaim. Datacenter-level offer granularity is not exposed by the `lowestPrice` query; constrain placement with `DataCenterIDs`.

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

## Capability matrix

| Resource | Transport | Coverage |
|----------|-----------|----------|
| Pods (CRUD, stop/resume, fallback, timing, diagnostics) | REST | Full |
| Serverless jobs (run/runsync/status/cancel/retry/purge/health/stream) | REST (api.runpod.ai) | Full |
| GPU types / availability / offers | GraphQL | Query only |
| Network volumes | REST | Full CRUD |
| Container registry auths | REST | Create/List/Delete |
| Secrets | REST | Full CRUD |
| Pod SSH diagnostics (`DiscoverSSHTarget`, `StreamPodCommand`) | GraphQL + system ssh | Dev/diagnostic only |
| Pod logs | — | Not exposed by RunPod's public API |
| Serverless endpoints / templates CRUD | — | Not implemented (no consumer) |

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
go test ./...                                    # unit tests (mock servers, no credentials)
RUNPOD_API_KEY=... go test -tags live ./...      # + live API integration tests
```

Live tests are excluded by the `live` build tag and additionally skip when `RUNPOD_API_KEY` is unset. The spot-pod live test creates a real (cheap) pod and is further gated on `RUNPOD_LIVE_SPOT=1`.

### Fake server for consumers

The `runpodtest` package is an in-process fake RunPod API — pods CRUD with per-GPU-type stock-out injection, network volumes, registry auths, the job lifecycle, gpuTypes queries, and one-shot 429/500 fault injection:

```go
srv := runpodtest.New()
defer srv.Close()
client := srv.MustClient()

srv.SetGPUStockOut("NVIDIA GeForce RTX 4090", true)
srv.FailNext(429, `{"error":"rate limited"}`, "1")
srv.CompleteJob(endpointID, jobID, myOutput)
```

## License

MIT
