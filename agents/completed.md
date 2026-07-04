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
