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
