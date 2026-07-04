<!-- runpod-go-sdk issue tracker — ACTIVE issues -->

> One `# #<id>: <name>` section per issue, separated by `---` lines; section anchor for
> tooling is a line starting with `# #`. IDs are stable for an issue's whole lifecycle and
> share ONE per-repo id space across progress.md / future.md / completed.md; new issues take `next_id` below and bump it.
> CONCURRENT EDITS: only ever edit/append your own issue's section with targeted string
> replacement — never rewrite the whole file.

Current and planned work items for runpod-go-sdk

next_id: 10

---

# #1: GPU types query with CUDA version filtering

**Status:** completed

Add ListGPUTypes and related methods to query available GPU types, filtering by minimum CUDA version. This enables gen-orchestrator to find machines that can run a container built for a specific CUDA version (e.g., 'this container needs CUDA 12.8, find machines that support 12.8+').

## Tasks
- [x] Add GraphQL client support (the SDK currently only uses REST)
- [x] Define GPUTypeFilter input struct
- [x] Define GPUTypeWithAvailability response struct (extends existing GPUType)
- [x] Implement ListGPUTypes(ctx, filter) - basic query without availability
- [x] Implement ListAvailableGPUs(ctx, minCudaVersion, gpuCount) - with lowestPrice/availability
- [x] Implement GetGPUType(ctx, gpuTypeID) - single GPU lookup
- [x] Add minCudaVersion/allowedCudaVersions to CreatePodRequest for pod creation
- [x] Update existing pod creation methods to support CUDA version constraints
- [x] Add tests with mock GraphQL responses
- [x] Update README with GPU query examples

## Use case

```json
{
  "scenario": "A tenant container is built with CUDA 12.8. When scheduling, we need to find RunPod machines that have CUDA 12.8+ drivers to ensure backward compatibility.",
  "constraint": "CUDA is backward compatible: a host with CUDA 13.0 can run 12.8 containers, but a host with CUDA 12.6 cannot.",
  "goal": "Query RunPod API to get list of GPU types that support a minimum CUDA version, with pricing and availability info."
}
```

## Api research

```json
{
  "endpoint": "https://api.runpod.io/graphql",
  "notes": "RunPod uses GraphQL for GPU queries. The SDK currently uses REST API (rest.runpod.io/v1) for pods/jobs, but GPU type queries require GraphQL.",
  "gpuTypes_query": {
    "description": "The gpuTypes query returns GPU types. CUDA filtering is done via the lowestPrice field's input parameters.",
    "cuda_params": [
      "minCudaVersion (String) - minimum CUDA version required",
      "allowedCudaVersions ([String]) - explicit list of allowed CUDA versions"
    ],
    "example_query": "query {\n  gpuTypes {\n    id\n    displayName\n    memoryInGb\n    secureCloud\n    communityCloud\n    lowestPrice(input: { gpuCount: 1, minCudaVersion: \"12.8\" }) {\n      minimumBidPrice\n      uninterruptablePrice\n      stockStatus\n    }\n  }\n}",
    "filtering_behavior": "If lowestPrice returns null or stockStatus is unavailable for a given minCudaVersion, that GPU type doesn't have machines supporting that CUDA version."
  },
  "pod_creation": {
    "description": "When creating pods, allowedCudaVersions can be passed to ensure the allocated machine supports the required CUDA.",
    "example": "podRentInterruptable(input: { ..., allowedCudaVersions: [\"12.8\", \"13.0\"] })"
  }
}
```

## Proposed api

```json
{
  "types": [
    {
      "name": "GPUTypeFilter",
      "fields": {
        "IDs": "[]string - filter to specific GPU type IDs",
        "MinCudaVersion": "string - minimum CUDA version (e.g., '12.8')",
        "AllowedCudaVersions": "[]string - explicit list of allowed versions",
        "SecureCloud": "*bool - filter to secure cloud only",
        "CommunityCloud": "*bool - filter to community cloud only"
      }
    },
    {
      "name": "GPUTypeWithAvailability",
      "fields": {
        "GPUType": "embedded GPUType struct",
        "StockStatus": "string - 'available', 'low', 'unavailable'",
        "AvailableCount": "int - number of available GPUs"
      }
    }
  ],
  "methods": [
    {
      "signature": "func (c *Client) ListGPUTypes(ctx context.Context, filter *GPUTypeFilter) ([]GPUType, error)",
      "description": "List all GPU types, optionally filtered by CUDA version and cloud type. Returns basic GPU info without availability."
    },
    {
      "signature": "func (c *Client) ListAvailableGPUs(ctx context.Context, minCudaVersion string, gpuCount int) ([]GPUTypeWithAvailability, error)",
      "description": "List GPU types that have available capacity for the given CUDA version. Returns availability and pricing info."
    },
    {
      "signature": "func (c *Client) GetGPUType(ctx context.Context, gpuTypeID string) (*GPUType, error)",
      "description": "Get details for a specific GPU type by ID (e.g., 'NVIDIA GeForce RTX 4090')."
    }
  ]
}
```

## Priority

high

---

# #2: Pod Diagnostics API (No Pod-Log Dependency)

**Status:** completed

Research summary (February 11, 2026):
- RunPod REST OpenAPI (`https://rest.runpod.io/v1/openapi.json`) documents pod lifecycle endpoints (`/pods`, `/pods/{podId}`, start/stop/reset/restart/update) but no pod log endpoint.
- Hitting `GET https://rest.runpod.io/v1/pods/{id}/logs` returns a route-not-found error (path/method does not exist).
- RunPod GraphQL validates that `Pod` has no `logs` field (`Cannot query field "logs" on type "Pod"`).

Implication:
- Programmatic pod container log retrieval is not a supported public API capability right now, and may remain unsupported.
- The SDK must not introduce new log-fetch features that depend on undocumented endpoints.

Goal:
- Make diagnostics first-class in SDK using supported fields (status/runtime/machine/lastStatusChange).
- Keep logs explicitly out of scope for orchestration-critical paths.

## Tasks
- [x] Add `GetPodOptions` and `GetPodWithOptions` to typed pod API surface
- [x] Add `PodDiagnostics` struct and `GetPodDiagnostics` helper
- [x] Add explicit capability error (`ErrCapabilityNotAvailable`) for unsupported provider features
- [x] Update `GetPodLogs` semantics:
  - mark deprecated in docs
  - return `ErrCapabilityNotAvailable` when endpoint is unavailable
  - do not treat missing `/pods/{id}/logs` as a transient retryable error
- [x] Add `GetProviderFeatureSupport` API (with `PodLogsAPI=false` for RunPod public API)
- [x] Add unit tests for route-not-found and capability errors
- [x] Update README: clearly separate "pod lifecycle diagnostics" from "pod logs" and note current provider limitation
- [x] Add explicit non-goal note in README/progress: no new SDK log streaming/tailing implementation without a documented supported RunPod API
- [x] Add integration test matrix (best-effort): verify diagnostics fields under CREATED/RUNNING/EXITED and includeMachine toggles

## Proposed api

```json
{
  "types": [
    {
      "name": "PodDiagnostics",
      "fields": {
        "PodID": "string",
        "DesiredStatus": "string",
        "LastStatusChange": "string",
        "RuntimeReady": "bool (derived: runtime != nil and runtime.ports/publicIp available)",
        "Runtime": "PodRuntime (typed snapshot)",
        "Machine": "Machine (typed snapshot, optional includeMachine)",
        "DataCenterID": "string (best-effort from machine/network volume)",
        "PublicIP": "string",
        "PortMappings": "map[string]int",
        "NetworkVolumeID": "string",
        "ProviderReason": "string (best-effort from API fields)"
      }
    },
    {
      "name": "ProviderFeatureSupport",
      "fields": {
        "PodLogsAPI": "bool (currently always false for RunPod public API)",
        "Reason": "string"
      }
    }
  ],
  "methods": [
    {
      "signature": "func (c *Client) GetPodWithOptions(ctx context.Context, podID string, opts *GetPodOptions) (*Pod, error)",
      "description": "Typed support for includeMachine/includeNetworkVolume/includeSavingsPlans/includeTemplate/includeWorkers query params."
    },
    {
      "signature": "func (c *Client) GetPodDiagnostics(ctx context.Context, podID string) (*PodDiagnostics, error)",
      "description": "Returns a normalized diagnostics snapshot for scheduler/orchestrator startup and disconnect troubleshooting."
    },
    {
      "signature": "func (c *Client) GetProviderFeatureSupport(ctx context.Context) ProviderFeatureSupport",
      "description": "Returns current SDK understanding of pod log availability via public API."
    }
  ]
}
```

## Priority

high

---

# #3: SDK-owned pod-create fallback: per-GPU-type fan-out + typed stock-out errors

**Status:** open

Audit 2026-07 (greenfield verdict). tensorhub discovered (its issue #350) that RunPod's REST `POST /pods` does NOT walk `gpuTypeIds` when the first type has no stock — it returns 500 "no instances available". The consumer had to reimplement a per-type fan-out loop plus a per-release failure tracker (~100 lines in tensorhub `pod.go` + `pod_failure_tracker.go`). That is provider-protocol knowledge and belongs in the SDK, not in every consumer.

Design: `CreatePodWithFallback(ctx, req, candidates []string) (*Pod, error)` (or make plain `CreatePod` do it when `len(GPUTypeIDs) > 1`): try candidates one at a time, classify each failure, return a typed `*NoCapacityError{GPUTypeID, DataCenterIDs}` per attempt and an aggregate error listing what was tried. Consumers then keep only policy (which candidates, failure memory), not protocol.

## Tasks
- [ ] Add typed `NoCapacityError` (detect RunPod's 500 "no instances available" / "no resources" bodies)
- [ ] Implement per-type fan-out in the SDK with context-aware early exit
- [ ] Optional pluggable `CandidateFilter` hook so callers can skip recently-failed types
- [ ] Mock-server tests: first type 500s, second succeeds; all fail -> aggregate error
- [ ] Migrate tensorhub's loop onto it and delete the consumer-side copy

---

# #4: Delete dead/speculative API surface

**Status:** open

Audit 2026-07. Large parts of the SDK are aspirational coverage that nothing calls, was never finished (types with zero methods), or was never runnable. Pre-launch, hard-cut:

- Types with NO methods anywhere: `Endpoint`, `CreateEndpointRequest`, `UpdateEndpointRequest`, `Template`, `CreateTemplateRequest`, `UpdateTemplateRequest`, `UpdatePodRequest`, `WebhookConfig`, `Datacenter`, `AccountInfo`, `CreateNetworkVolumeRequest` (keep `NetworkVolume` — used by pod includes; or implement the methods, see #5)
- `ValidationErrors` slice type: never constructed
- `TimeoutError` / `NewTimeoutError`: never constructed, so `IsTimeoutError` can never be true (real timeouts surface as `NetworkError`)
- `GPUType.CostPerHour` and `GPUType.Available`: never populated (the GraphQL query doesn't select them) — misleading
- `deriveAvailableCount` invents counts (2/1) from stock status strings — return the status, not fake numbers; drop `GPUTypeWithAvailability.AvailableCount`
- `GetPodLogs`: deprecated, endpoint doesn't exist; `GetPodDiagnostics`/feature-support already model this — delete
- `ListRunningPods`/`ListStoppedPods`/`ListPodsByStatus`/`FindPodByName`/`GetPodStatus`: trivial client-side wrappers over `ListPods`; callers can filter
- Getter boilerplate `GetBaseURL`/`GetServerlessBaseURL`/`GetGraphQLBaseURL`/`IsDebugEnabled`/`GetAPIKey` on already-exported fields
- Serverless jobs surface is unused by tensorhub but is real, tested API coverage — KEEP, but drop the thin conveniences `QuickRun`, `SubmitMultipleJobs`, `WaitForMultipleJobs`, `StreamResultsContinuous`, `compareOutputs` unless a consumer materializes
- Empty `cmd/` directory

## Tasks
- [ ] Delete the symbols above; `go build ./... && go test ./...`
- [ ] Prune README sections for deleted surface
- [ ] Re-check against tensorhub (only consumer) before each deletion

---

# #5: Network-volume + container-registry-auth coverage (what the real consumer hand-rolls)

**Status:** open

Audit 2026-07. tensorhub hand-rolls, next to the SDK, its own HTTP plumbing for exactly the two resources the SDK lacks:

- `registry_auth.go` (~415 lines): REST `POST/GET/DELETE /containerregistryauth` + its own restRequest helper, plus auth-failure heuristics (`IsRunpodRegistryAuthError` string matching)
- `volume_manager.go` + `api.go`: GraphQL `myself { networkVolumes }` and `updateNetworkVolume` with a second, duplicate client type — the ONLY reason tensorhub still owns a GraphQL client

RunPod's REST API now exposes network volumes (`GET/POST/PATCH/DELETE /networkvolumes`) and registry auths. Adding both lets tensorhub delete its local `Client` entirely and construct one `runpodsdk.Client`.

## Tasks
- [ ] `ListNetworkVolumes` / `CreateNetworkVolume` / `UpdateNetworkVolume` (resize) / `DeleteNetworkVolume` via REST; verify REST parity vs GraphQL fields (dataCenterId, size)
- [ ] `CreateContainerRegistryAuth` / `ListContainerRegistryAuths` / `DeleteContainerRegistryAuth`
- [ ] Typed detection for registry-auth pod-create failures (replace consumer string matching)
- [ ] Mock-server tests + RUNPOD_API_KEY-gated live test
- [ ] Migrate tensorhub off its local Client/GraphQLRequest and delete it

---

# #6: Coherent client core: error model, retry policy, construction

**Status:** open

Audit 2026-07. The client core mostly works but has fossil edges:

- `NewClient` panics on empty API key — return `(*Client, error)` or defer failure to first call with a typed AuthError
- Client fields are all exported and mutable (`APIKey`, `HTTPClient`, ...) while also having functional options AND getter methods — pick one: unexported fields + options
- Retry is linear (`RetryDelay * attempt`), no jitter, ignores `Retry-After` on 429, and the retryable-error path re-POSTs (partially fixed by the 2026-07 audit PR: POSTs no longer retry on 5xx; honoring Retry-After on 429 retries remains)
- Errors: seven hand-rolled error types with `New*`/`Is*` constructor+predicate boilerplate. Collapse to `APIError` (with `errors.Is` sentinel support: `ErrNotFound`, `ErrNoCapacity`, `ErrRateLimited`...) + `ValidationError`; drop the rest
- `buildURL`'s `/v2/` string-sniffing to route serverless vs REST — make the serverless surface explicit (e.g. `c.Serverless()` sub-client or explicit base per method group)
- `Job.Input/Output/Stream interface{}` — use `json.RawMessage` so callers unmarshal into their own types
- `WaitForPodStatus(maxAttempts)` and hardcoded 5s polls — take a poll interval/deadline like `WaitForPodReady` does; or delete in favor of `WaitForPodReady`
- `RunSync` can outlive the client's 30s HTTP timeout for long jobs — document or set per-call timeout

## Tasks
- [ ] Constructor returns error; unexport config fields
- [ ] Exponential backoff + jitter + Retry-After
- [ ] Error model collapse w/ sentinel `errors.Is` support
- [ ] Explicit serverless routing; kill URL sniffing
- [ ] `json.RawMessage` job payloads

---

# #7: One GPU catalog: SDK-owned SKU data (VRAM, SM capability, Blackwell)

**Status:** open

Audit 2026-07. GPU knowledge is split and diverging: the SDK has `defaultGPUTypeIDLadder` (id + VRAM only, no A40/A100-SXM4/H100-PCIe), tensorhub has `KnownGPUs` (id + VRAM + SM capability, no 3070/3080/4080). Both are static; neither knows B200/Blackwell datacenter SKUs. A Vast.ai-style sibling provider would need the same table again.

The SDK should own one catalog: `GPUSpec{ID, DisplayName, VRAMGB, SMCapability, Consumer bool}` + `GPUCatalog()` + selection helpers (`GPUsWithAtLeast(vramGB, smMin)` fallback-ordered), refreshable from `ListGPUTypes` for stock/price. Include RTX 5090 (SM 120), B200, RTX 6000 Ada/Blackwell Pro as RunPod lists them.

## Tasks
- [ ] Define catalog + selection helpers in SDK; delete `defaultGPUTypeIDLadder`/`DefaultGPUTypeID` in favor of them
- [ ] Verify type-ID strings against live `gpuTypes` query (RUNPOD_API_KEY-gated test)
- [ ] Migrate tensorhub `gpu_selection.go` onto the SDK catalog (keep policy — chain ordering — in the consumer or accept ordering as parameter)

---

# #8: Spot/interruptible + price-aware placement

**Status:** open

Audit 2026-07. `CreateSpotPod` just flips `Interruptible` — there is no bid price (`BidPerGPU` exists only as a commented-out validation block), no spot-reclaim semantics, and no price-aware pod placement. `ListAvailableGPUs` already returns `minimumBidPrice`/`uninterruptablePrice` but nothing connects it to pod creation. Needed for cost-optimized autoscaling and for provider parity with a future Vast.ai sibling (offer/bid model).

## Tasks
- [ ] `BidPerGPU` on CreatePodRequest, verified against REST (or documented as GraphQL-only w/ fallback)
- [ ] Surface interruptible price + stock in one "offers"-style query helper (GPU type x cloud x datacenter)
- [ ] Document/detect spot reclaim: what the API reports when a spot pod is preempted (desiredStatus/runtime transitions)
- [ ] Live-gated test creating + reclaim-polling a cheap spot pod

---

# #9: DX: fake RunPod server, one-command test story, README truthfulness

**Status:** open

Audit 2026-07. Tests exist and are decent (mock httptest servers for jobs/gpu-types/diagnostics; validation unit tests) but consumers can't reuse any of it: every consumer builds its own mocks. And the README documents surface that doesn't exist or is slated for deletion.

## Tasks
- [ ] `runpodtest` package: in-process fake implementing the endpoints the SDK covers (pods CRUD w/ stock-out injection, jobs lifecycle, 429/500 fault injection) for consumers to point a Client at
- [ ] Fold root-level `cpu_pods_test.go`/`pods_timing_test.go` and `tests/` into one layout; document `go test ./...` (unit) vs `RUNPOD_API_KEY=... go test -tags live` (integration)
- [ ] README audit: remove/rewrite sections describing deleted or never-implemented surface (endpoints/templates CRUD, pod logs); add a truthful capability matrix (REST vs GraphQL per resource)
- [ ] Godoc pass on exported symbols after #4/#6 deletions
