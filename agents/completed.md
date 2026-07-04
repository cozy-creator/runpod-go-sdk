<!-- runpod-go-sdk issue tracker — COMPLETED issues -->

> Finished issues move here from progress.md verbatim, with **Status:** DONE and completion notes.
> IDs share the per-repo id space defined in progress.md (next_id lives there).

---

# #3: SDK-owned pod-create fallback: per-GPU-type fan-out + typed stock-out errors

**Status:** DONE (2026-07-04)

Shipped: `CreatePodWithFallback` + `NoCapacityError`/`ErrNoCapacity` sentinel + `FallbackExhaustedError` aggregate; plain `CreatePod` fans out automatically when `len(GPUTypeIDs) > 1`. 4xx/transport errors abort the fan-out; 5xx continues. Tensorhub migration deferred to the tensorhub-tracker adoption issue (tensorhub #549).

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

Shipped: REST `ListNetworkVolumes`/`GetNetworkVolume`/`CreateNetworkVolume`/`UpdateNetworkVolume`/`DeleteNetworkVolume` and `ListContainerRegistryAuths`/`CreateContainerRegistryAuth`/`DeleteContainerRegistryAuth`, plus `IsRegistryAuthError` typed detection (capacity errors and SDK API-key errors excluded). `NetworkVolume.DataCenterID` json tag fixed to `dataCenterId` (REST parity). Mock CRUD tests + RUNPOD_API_KEY-gated live list tests. Tensorhub migration deferred to the tensorhub-tracker adoption issue (tensorhub #549).

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

---

# #7: One GPU catalog: SDK-owned SKU data (VRAM, SM capability, Blackwell)

**Status:** DONE (2026-07-04)

Shipped: `GPUSpec` catalog (23 SKUs, Ampere→Blackwell incl. RTX 5090 SM120, B200 SM100, RTX PRO 6000 Blackwell, H200) + `GPUCatalog()`/`GPUSpecByID()`/`GPUsWithAtLeast(vram, smMin)`/`GPUTypeIDs()` helpers, fallback-preference ordered. `defaultGPUTypeIDLadder`/`DefaultGPUTypeID` deleted. Live-gated `TestGPUCatalogIDsLive` verifies every ID against the real gpuTypes query. Tensorhub `gpu_selection.go` migration deferred to the tensorhub-tracker adoption issue (tensorhub #549).

Audit 2026-07. GPU knowledge is split and diverging: the SDK has `defaultGPUTypeIDLadder` (id + VRAM only, no A40/A100-SXM4/H100-PCIe), tensorhub has `KnownGPUs` (id + VRAM + SM capability, no 3070/3080/4080). Both are static; neither knows B200/Blackwell datacenter SKUs. A Vast.ai-style sibling provider would need the same table again.

The SDK should own one catalog: `GPUSpec{ID, DisplayName, VRAMGB, SMCapability, Consumer bool}` + `GPUCatalog()` + selection helpers (`GPUsWithAtLeast(vramGB, smMin)` fallback-ordered), refreshable from `ListGPUTypes` for stock/price. Include RTX 5090 (SM 120), B200, RTX 6000 Ada/Blackwell Pro as RunPod lists them.

## Tasks
- [x] Define catalog + selection helpers in SDK; delete `defaultGPUTypeIDLadder`/`DefaultGPUTypeID` in favor of them
- [x] Verify type-ID strings against live `gpuTypes` query (RUNPOD_API_KEY-gated test)
- [ ] (deferred to tensorhub tracker adoption issue) Migrate tensorhub `gpu_selection.go` onto the SDK catalog (keep policy — chain ordering — in the consumer or accept ordering as parameter)

---

# #8: Spot/interruptible + price-aware placement

**Status:** DONE (2026-07-04)

Shipped: `BidPerGPU` on CreatePodRequest (validated: positive + requires Interruptible; REST `bidPerGpu`), `ListGPUOffers` (GPU type x cloud offers via aliased per-cloud lowestPrice query, sorted by on-demand price; datacenter granularity documented as not exposed), spot-reclaim semantics documented on CreateSpotPod (preempted pods report desiredStatus=EXITED, runtime cleared; no dedicated signal). Live-gated `TestSpotPodReclaimLive` (extra RUNPOD_LIVE_SPOT=1 gate since it costs money) creates a bid spot pod, verifies Interruptible, terminates.

Audit 2026-07. `CreateSpotPod` just flips `Interruptible` — there is no bid price (`BidPerGPU` exists only as a commented-out validation block), no spot-reclaim semantics, and no price-aware pod placement. `ListAvailableGPUs` already returns `minimumBidPrice`/`uninterruptablePrice` but nothing connects it to pod creation. Needed for cost-optimized autoscaling and for provider parity with a future Vast.ai sibling (offer/bid model).

## Tasks
- [x] `BidPerGPU` on CreatePodRequest, verified against REST (or documented as GraphQL-only w/ fallback)
- [x] Surface interruptible price + stock in one "offers"-style query helper (GPU type x cloud x datacenter)
- [x] Document/detect spot reclaim: what the API reports when a spot pod is preempted (desiredStatus/runtime transitions)
- [x] Live-gated test creating + reclaim-polling a cheap spot pod

---

# #9: DX: fake RunPod server, one-command test story, README truthfulness

**Status:** DONE (2026-07-04)

Shipped: `runpodtest` package — in-process fake covering pods CRUD w/ per-GPU-type stock-out injection, network volumes, registry auths, job lifecycle (CompleteJob/FailJob controls), gpuTypes GraphQL, and one-shot 429/500 fault injection with Retry-After; exercised by its own e2e test suite. Test layout unified: `tests/` folded into the package root; live tests behind `//go:build live` + env gating (`go test ./...` = unit, `RUNPOD_API_KEY=... go test -tags live ./...` = integration; spot test extra-gated on RUNPOD_LIVE_SPOT=1). README rewritten truthfully with a REST/GraphQL capability matrix. Godoc pass done on all exported symbols.

Audit 2026-07. Tests exist and are decent (mock httptest servers for jobs/gpu-types/diagnostics; validation unit tests) but consumers can't reuse any of it: every consumer builds its own mocks. And the README documents surface that doesn't exist or is slated for deletion.

## Tasks
- [x] `runpodtest` package: in-process fake implementing the endpoints the SDK covers (pods CRUD w/ stock-out injection, jobs lifecycle, 429/500 fault injection) for consumers to point a Client at
- [x] Fold root-level `cpu_pods_test.go`/`pods_timing_test.go` and `tests/` into one layout; document `go test ./...` (unit) vs `RUNPOD_API_KEY=... go test -tags live` (integration)
- [x] README audit: remove/rewrite sections describing deleted or never-implemented surface (endpoints/templates CRUD, pod logs); add a truthful capability matrix (REST vs GraphQL per resource)
- [x] Godoc pass on exported symbols after #4/#6 deletions
