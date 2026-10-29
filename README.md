# runpod-go-sdk

Go client for RunPod's **REST v2 control plane** and separate **Serverless inference API**. Resource management, catalogs, billing, and pod logs use `https://api.runpod.io/v2`. Inference jobs use `https://api.runpod.ai/v2`.

REST v1 and GraphQL support have been removed. This is a breaking SDK migration: requests and responses use native v2 fields. Old prepared purchase bodies are rejected before HTTP. See the [migration notes](docs/rest-v2-migration-20261028.md) for the official contract and unsupported legacy capabilities.

## Why use this SDK instead of the official one?

For general REST v2 access, the [official Runpod Go SDK](https://github.com/runpod/go-sdk) is a valid starting point and covers more management API areas. This library packages the purchase and evidence contracts that TensorHub currently relies on:

- **Durable purchases:** prepare and inspect a validated create body, persist its exact bytes before spending, then execute those bytes unchanged. Typed capacity failures allow fallback; ambiguous outcomes require readback rather than another purchase.
- **Exact money and qualified billing evidence:** decode rates directly to integer USD micros, preserve bounded raw billing responses, and validate the pod identity, requested/resolved window, record counts and amount totals before using a bill. Missing facts do not become zero-cost evidence.
- **Quota and observation handling:** retain the longest exhausted quota window across calls, including after cancellation; follow complete cursor walks and refuse incomplete or stalled snapshots before treating a resource as absent. Inventory helpers retain cloud/count/location scope and per-result failures.

These are important to our consumer, but they are not capabilities that only our SDK could provide. The official client already avoids retrying ambiguous POST/PATCH transport and server failures, accepts raw request bodies, and retains raw response bytes. Its generated money fields use floating-point types, paginated list operations return one page, and retry state is per request. A consumer can build the additional contracts above around it. Replacing this library would mean preserving and qualifying those contracts, rather than just changing imports.

Serverless job helpers in the same client are a convenience. The official REST v2 client keeps resource management separate from its older `pkg/sdk` Serverless invocation module. This library does not claim complete Runpod API coverage or official status.

### Historical scope

The official repository dates to 2023, when its `pkg/sdk` client handled Serverless jobs. Its generated REST v2 resource client was [added on October 13, 2026](https://github.com/runpod/go-sdk/commit/d7cd5e28594e190ada4564133bb7d725faa1d617). This project [already implemented Pod management in June 2025](https://github.com/cozy-creator/runpod-go-sdk/commit/31db935). The repository’s age therefore does not mean today’s management client was available when this project began; the current official client does support Pod management.

Comparison checked against official **v2.1.0** on October 29, 2026: [retry transport](https://github.com/runpod/go-sdk/blob/d261aaf1f3cb4cd73c4c73fef1631e6d5a222958/transport/retry.go), [generated types](https://github.com/runpod/go-sdk/blob/d261aaf1f3cb4cd73c4c73fef1631e6d5a222958/lib/types/types.gen.go), [raw Pod requests](https://github.com/runpod/go-sdk/blob/d261aaf1f3cb4cd73c4c73fef1631e6d5a222958/lib/pods/pods.gen.go), and [billing responses](https://github.com/runpod/go-sdk/blob/d261aaf1f3cb4cd73c4c73fef1631e6d5a222958/lib/billing/billing.gen.go).

## Install

```bash
go get github.com/cozy-creator/runpod-go-sdk
```

## Create a pod

```go
client, err := runpod.NewClient(os.Getenv("RUNPOD_API_KEY"))
if err != nil { return err }

req := &runpod.CreatePodRequest{
    Name:  "worker-1",
    Image: "tensorhub/worker:torch2.13.0-cu130-cp312-linux-x86",
    Disk:  50,
    Cloud: "SECURE",
    GPU: &runpod.CreateGPUConfig{
        ID: "NVIDIA GeForce RTX 4090", Count: 1,
        MinCUDAVersion: "12.8",
        MinRAMPerGPU: 32, MinVCPUCountPerGPU: 4,
    },
}
pod, err := client.CreatePod(ctx, req)
```

CPU pods set `CPU: &runpod.CreateCPUConfig{ID: "cpu5c", VCPUCount: 4}` and omit `GPU`. Exactly one compute configuration is required. CPU memory is derived by RunPod from the selected flavor; vCPU count must be a power of two.

Persistent disks use `Mounts.Persistent`. Network and global volumes use `Mounts.Network` and `Mounts.Global`, each containing `VolumeMount{VolumeID, Path}`. A persistent disk cannot be combined with network/global mounts or CPU pods. Network volumes require Secure Cloud and compatible data center placement.

`CreatePodWithFallback(ctx, req, candidates, opts)` explicitly tries one GPU type at a time. It advances only after a typed `ErrNoCapacity`; transport failures and unclassified server errors abort because a pod may already exist. Spot creation and bidding are unavailable in v2 and have no SDK methods.

For durable purchase bookkeeping:

```go
body, err := client.PrepareCreatePod(req)
// Persist body and its purchase obligation before calling the provider.
pod, err := client.ExecuteCreatePod(ctx, body) // exact recorded bytes
```

`InspectPreparedCreatePod` validates those bytes without changing them. `FindPodsByName` scans every cursor page and returns all exact name matches. Names are not unique; callers own adoption decisions.

## Pod reads and lifecycle

`GetPod` returns native `Status`, `Image`, `Cost`, `GPU`/`CPU`, `Mounts`, `StartedAt`, and `Runtime` fields. Provider states (`PROVISIONING`, `STARTING`, `RUNNING`, `EXITED`, `ERROR`, `TERMINATED`) remain distinct. Runtime is null in list responses; fetch a single pod for live metrics. Provider RUNNING status does not prove application readiness.

- `ListPods(ctx, opts)` follows cursor pagination through the remaining result set; options are `Limit`, `Cursor`, and `IncludeClusterPods`.
- `GetPodReadback` preserves one independently timed REST observation and validates identity. Malformed successful responses match `ErrIncompleteReadback`; only a provider 404 proves API absence.
- `ValidatedPublicTCPEndpoints` checks native runtime port mappings, preserving null public IP/port as unavailable.
- `UpdatePod` sends PATCH; `RenamePod` changes only the name.
- `StopPod`, `ResumePod`, and `RestartPod` use the v2 action endpoint. `TerminatePod` uses DELETE.
- `GetPodDiagnostics`, `GetPodLifecycleObservation`, and `GetPodTerminalError` report evidence available through v2. Removed telemetry, machine IDs, exit codes, and spot flags are not synthesized.
- `WaitForPodReady` and `PodTimingSnapshot` preserve provider timestamps and observed elapsed intervals. They do not invent image-pull durations.
- `DiscoverSSHTarget` uses native SSH connection details; `StreamPodCommand` invokes system SSH for explicit diagnostics.

## Catalogs and inventory

```go
offers, err := client.ListGPUOffers(ctx, &runpod.GPUOfferFilter{
    CloudType: "SECURE", GPUCount: 2, MinCudaVersion: "12.8",
    InStockOnly: true,
})
gpus, err := client.ListGPUTypes(ctx, &runpod.GPUTypeFilter{CloudType: "SECURE"})
cpu, err := client.GetCPUOffer(ctx, runpod.CPUOfferRequest{
    InstanceID: "cpu5c-4-8", DataCenterID: "EU-RO-1",
})
dcs, err := client.ListDataCenters(ctx, nil)
```

Catalog availability reads explicitly use `include=AVAILABILITY&product=POD`. GPU cloud, count, and CUDA constraints scope stock. An empty offer cloud filter reads Secure and Community separately. GPU `maxCount` is a machine-size ceiling, never a current stock count.

Catalog GPU rates are per GPU; CPU rates are per vCPU. Normalized `GPUOffer`/`CPUOffer` prices multiply the exact decimal rate into the requested whole-pod rate with overflow checks. `PriceSource` identifies catalog list pricing; a pod's `Cost` is separate provider evidence. There are no spot-floor prices, bandwidth floors, or CPU disk ceilings in v2.

`ObserveInventory` returns ordered per-request results and scoped errors. Within a pass, identical URLs share exact bytes; reads run sequentially with one HTTP attempt, and a rate limit/cancellation stops further HTTP. The caller owns refresh cadence. `NetworkVolumes` optionally adds a separately qualified volume snapshot. Missing/null requested lists or malformed required rows are errors, not known absence.

The static `GPUCatalog`, `GPUsWithAtLeast`, and `GPUSpecByID` helpers remain available for GPU hardware metadata. Runtime stock and prices come from v2 catalogs.

## Billing

```go
history, err := client.GetPodBillingHistory(ctx, podID, startUTC, endUTC)
// Preserve NormalizedQuery, Query, and RawResponse as provider evidence.
for _, record := range history.Records {
    fmt.Println(record.BucketStart, record.BucketEnd, record.AmountUSDMicros)
}
```

Billing uses native v2 `records` and `metadata`, including total, GPU, CPU, and disk amounts. Signed decimal amounts round directly to the nearest USD micro without passing through float64. The checked sum of individually rounded records and the independently rounded metadata total are retained separately. Responses are bounded to 16 MiB; failures preserve a typed `PodBillingEvidenceError` and complete bounded evidence.

The provider's echoed query must match the requested hourly window snapped outward to bucket edges. Bucket boundaries are not billed runtime. V2 does not expose billed duration or account balance; missing duration must remain unknown. An empty qualified record list is zero-cost evidence, not provider settlement finality.

## Volumes, registries, secrets

```go
volume, err := client.CreateNetworkVolume(ctx, &runpod.CreateNetworkVolumeRequest{
    Name: "models", Size: 100, DataCenter: "EU-RO-1", Type: "STANDARD",
})
volumes, err := client.ListNetworkVolumes(ctx) // follows cursor pages
registry, err := client.CreateContainerRegistryAuth(ctx, &runpod.CreateContainerRegistryAuthRequest{
    Name: "my-registry", Username: "bob", Password: token,
})
```

Network volume CRUD uses `/network-volumes`; registry credentials use `/registries`. Secret CRUD uses `/account/secrets`, with immutable IDs for single-resource operations and PATCH for updates. `CreateOrUpdateSecret` resolves an exact name before updating its ID. Secret values and registry passwords are write-only.

## Serverless inference

`RunAsync`, `RunSync`, `GetJobStatus`, `WaitForJobCompletion`, `RunAndWait`, `StreamResults`, `CancelJob`, `RetryJob`, `PurgeQueue`, and `GetHealth` use the separate inference service. Input/output/stream fields remain `json.RawMessage`.

The default HTTP timeout is 30 seconds, including `RunSync` and entire log streams. Configure a longer timeout or use asynchronous jobs when needed.

## Client configuration and errors

```go
client, err := runpod.NewClient(apiKey,
    runpod.WithBaseURL("https://api.runpod.io/v2"),
    runpod.WithServerlessBaseURL("https://api.runpod.ai"),
    runpod.WithTimeout(120*time.Second),
    runpod.WithMaxRetryAttempts(3),
    runpod.WithRetryDelay(time.Second),
    runpod.WithHTTPClient(customHTTPClient),
)
```

There is one control-plane base, including logs. HTTP errors carry `*APIError` and their exact response bytes. Match `ErrNotFound`, `ErrUnauthorized`, `ErrRateLimited`, and `ErrNoCapacity` with `errors.Is`.

Idempotent requests retry transient server/transport failures with exponential backoff and jitter. POSTs never retry those ambiguous failures. Rejected 429 requests honor the longest `Retry-After` or exhausted `RateLimit` window. Later requests retain the cooldown, even after cancellation. Long waits return typed errors promptly. `Cooldown()` reports the control-plane budget; `ServerlessCooldown(endpointID)` reports independent inference endpoint budgets.

## Pod logs

`StreamPodLogs` reads v2 SSE container/system logs. Its stream owns the response body: call `Close` and give long-running streams a caller-owned cancellation context, typically with `WithTimeout(0)`.

`PodLogsOptions` supports `Source`, `Tail` (nil defaults to 100 per source, zero means live-only, maximum 5000), `Since`, and opaque `LastEventID`. Reconnect cursors take precedence over Since/Tail. IDs and timestamps can repeat; do not deduplicate by ID alone. The provider has no replay-complete marker and may withhold headers until a matching line exists. EOF/timeouts remain errors; callers own snapshot limits and reconnects. Logs are diagnostic evidence.

## Testing

```bash
go fmt ./...
go vet ./...
go build ./...
go test ./...
go test -race ./...
RUNPOD_API_KEY=... go test -tags live ./... # explicitly provided credentials, read-only by default
```

Live tests skip when credentials are unset. Tests that submit inference jobs or mutate queues additionally require `RUNPOD_LIVE_CREATE=1`. They never load `.env` automatically.

`runpodtest.New()` provides native REST v2 management and separate inference fixtures, cursor pages, per-GPU stock-out injection, pod logs, and one-shot HTTP fault injection. Removed resource paths return 404.

## License

MIT
