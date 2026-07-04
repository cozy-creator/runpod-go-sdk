<!-- runpod-go-sdk issue tracker — COMPLETED issues -->

> Finished issues move here from progress.md verbatim, with **Status:** DONE and completion notes.
> IDs share the per-repo id space defined in progress.md (next_id lives there).

---

# #3: SDK-owned pod-create fallback: per-GPU-type fan-out + typed stock-out errors

**Status:** DONE (2026-07-04)

Shipped: `CreatePodWithFallback` + `NoCapacityError`/`ErrNoCapacity` sentinel + `FallbackExhaustedError` aggregate; plain `CreatePod` fans out automatically when `len(GPUTypeIDs) > 1`. 4xx/transport errors abort the fan-out; 5xx continues. Tensorhub migration deferred to the tensorhub-tracker adoption issue.

Audit 2026-07 (greenfield verdict). tensorhub discovered (its issue #350) that RunPod's REST `POST /pods` does NOT walk `gpuTypeIds` when the first type has no stock — it returns 500 "no instances available". The consumer had to reimplement a per-type fan-out loop plus a per-release failure tracker (~100 lines in tensorhub `pod.go` + `pod_failure_tracker.go`). That is provider-protocol knowledge and belongs in the SDK, not in every consumer.

Design: `CreatePodWithFallback(ctx, req, candidates []string) (*Pod, error)` (or make plain `CreatePod` do it when `len(GPUTypeIDs) > 1`): try candidates one at a time, classify each failure, return a typed `*NoCapacityError{GPUTypeID, DataCenterIDs}` per attempt and an aggregate error listing what was tried. Consumers then keep only policy (which candidates, failure memory), not protocol.

## Tasks
- [x] Add typed `NoCapacityError` (detect RunPod's 500 "no instances available" / "no resources" bodies)
- [x] Implement per-type fan-out in the SDK with context-aware early exit
- [x] Optional pluggable `CandidateFilter` hook so callers can skip recently-failed types
- [x] Mock-server tests: first type 500s, second succeeds; all fail -> aggregate error
- [ ] (deferred to tensorhub tracker adoption issue) Migrate tensorhub's loop onto it and delete the consumer-side copy

---

# #5: Network-volume + container-registry-auth coverage (what the real consumer hand-rolls)

**Status:** DONE (2026-07-04)

Shipped: REST `ListNetworkVolumes`/`GetNetworkVolume`/`CreateNetworkVolume`/`UpdateNetworkVolume`/`DeleteNetworkVolume` and `ListContainerRegistryAuths`/`CreateContainerRegistryAuth`/`DeleteContainerRegistryAuth`, plus `IsRegistryAuthError` typed detection (capacity errors and SDK API-key errors excluded). `NetworkVolume.DataCenterID` json tag fixed to `dataCenterId` (REST parity). Mock CRUD tests + RUNPOD_API_KEY-gated live list tests. Tensorhub migration deferred to the tensorhub-tracker adoption issue.

Audit 2026-07. tensorhub hand-rolls, next to the SDK, its own HTTP plumbing for exactly the two resources the SDK lacks:

- `registry_auth.go` (~415 lines): REST `POST/GET/DELETE /containerregistryauth` + its own restRequest helper, plus auth-failure heuristics (`IsRunpodRegistryAuthError` string matching)
- `volume_manager.go` + `api.go`: GraphQL `myself { networkVolumes }` and `updateNetworkVolume` with a second, duplicate client type — the ONLY reason tensorhub still owns a GraphQL client

RunPod's REST API now exposes network volumes (`GET/POST/PATCH/DELETE /networkvolumes`) and registry auths. Adding both lets tensorhub delete its local `Client` entirely and construct one `runpodsdk.Client`.

## Tasks
- [x] `ListNetworkVolumes` / `CreateNetworkVolume` / `UpdateNetworkVolume` (resize) / `DeleteNetworkVolume` via REST; verify REST parity vs GraphQL fields (dataCenterId, size)
- [x] `CreateContainerRegistryAuth` / `ListContainerRegistryAuths` / `DeleteContainerRegistryAuth`
- [x] Typed detection for registry-auth pod-create failures (replace consumer string matching)
- [x] Mock-server tests + RUNPOD_API_KEY-gated live test
- [ ] (deferred to tensorhub tracker adoption issue) Migrate tensorhub off its local Client/GraphQLRequest and delete it

---

# #4: Delete dead/speculative API surface

**Status:** DONE (2026-07-04)

Deleted: Endpoint/Template/Webhook/Datacenter/AccountInfo/UpdatePodRequest types, ValidationErrors, TimeoutError, GPUType.CostPerHour/.Available, deriveAvailableCount + GPUTypeWithAvailability.AvailableCount, GetPodLogs, the trivial pod wrappers (ListRunningPods/ListStoppedPods/ListPodsByStatus/FindPodByName/GetPodStatus/WaitForPodStatus), client getters, and the jobs conveniences (QuickRun/SubmitMultipleJobs/WaitForMultipleJobs/StreamResultsContinuous/compareOutputs). CreateNetworkVolumeRequest kept — methods implemented in #5. README pruned to the surviving surface. cmd/ was never tracked in git (nothing to remove).

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
- [x] Delete the symbols above; `go build ./... && go test ./...`
- [x] Prune README sections for deleted surface
- [x] Re-check against tensorhub (only consumer) before each deletion

---

# #6: Coherent client core: error model, retry policy, construction

**Status:** DONE (2026-07-04)

NewClient returns (*Client, error); config fields unexported (options-only). Exponential backoff + jitter, Retry-After honored on 429 (APIError.RetryAfter). Error model collapsed to APIError + ValidationError (+ NoCapacityError/FallbackExhaustedError from #3) with ErrNotFound/ErrUnauthorized/ErrRateLimited/ErrNoCapacity sentinels via errors.Is. /v2/ URL-sniffing removed — serverless methods build absolute URLs against the serverless base. Job.Input/Output/Stream are json.RawMessage. WaitForPodStatus deleted in favor of WaitForPodReady; RunSync timeout interplay documented.

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
- [x] Constructor returns error; unexport config fields
- [x] Exponential backoff + jitter + Retry-After
- [x] Error model collapse w/ sentinel `errors.Is` support
- [x] Explicit serverless routing; kill URL sniffing
- [x] `json.RawMessage` job payloads
