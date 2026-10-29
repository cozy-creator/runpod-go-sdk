# RunPod REST v2 migration evidence

Checked 2026-10-28 against RunPod's published documentation and unauthenticated
live [OpenAPI schema](https://api.runpod.io/v2/openapi.json), version `2.0.0`.
Schema SHA-256: `92c13be196c3dd90f956c045e57bf3058d67ffb9c449fb998ddc045c8dbaf0d3`.
This is contract research, not evidence of authenticated execution, capacity,
billing settlement, release, or TensorHub deployment.

## Supported API boundary

RunPod announced REST v1 retirement for **2026-11-15** and GraphQL retirement for
**early 2027**, without a precise GraphQL date in its release notice. Use
`https://api.runpod.io/v2` for infrastructure management and remove both legacy
transports and their fallback paths. Bearer API-key authentication remains.
Sources: [release notice](https://docs.runpod.io/release-notes),
[v2 overview](https://docs.runpod.io/api-reference-v2/overview).

Other RunPod interfaces serve separate purposes:

| Interface | Use here |
| --- | --- |
| `https://api.runpod.ai/v2/{endpointId}/...` | Keep existing Serverless job submission/status/cancel/stream operations. This is the inference service, separately documented from management REST v2. |
| `https://{endpointId}.api.runpod.ai/{customPath}` | Optional direct HTTP access to a user's load balancing worker; not a replacement management API. |
| Datacenter-specific S3-compatible endpoints | Optional file transfer to network volumes without renting a Pod; separate S3 credentials and protocol. No SDK expansion needed for this migration. |
| SSH | Pod shell/file transfer; discover connection details through v2's `ssh` response. Proxy SSH lacks SCP/SFTP; direct SSH supports them. |
| Public model endpoints | Optional inference against RunPod-hosted models, using `api.runpod.ai`; not needed for Pod infrastructure. |
| RunPod MCP | An agent-facing wrapper over RunPod's APIs, plus a documentation server. It is not a better production Go SDK transport. |

Sources: [Serverless requests](https://docs.runpod.io/serverless/endpoints/send-requests),
[load balancing](https://docs.runpod.io/serverless/load-balancing/overview),
[S3-compatible storage](https://docs.runpod.io/storage/s3-api),
[SSH](https://docs.runpod.io/pods/configuration/use-ssh),
[Public Endpoints](https://docs.runpod.io/public-endpoints/overview),
[MCP](https://docs.runpod.io/get-started/mcp-servers).

## Wire contract changes

The authoritative field definitions below are the live
[OpenAPI schema](https://api.runpod.io/v2/openapi.json), especially
`CreatePodRequest`, `Pod`, `Mounts`, `Paginated`, `GpuType`, `CpuType`,
`ListPodBillingResponse`, and `ErrorResponse`. The
[migration guide](https://docs.runpod.io/api-reference-v2/migrate-from-v1)
provides the path-level map; prefer the schema for exact field types.

| Existing surface | v2 contract |
| --- | --- |
| Pod create | Nested `gpu:{id,count,...}` or `cpu:{id,vcpuCount}`; exactly one compute kind. `image`, `disk`, `cloud`, `registry`, `entrypoint`, `cmd`, `args`, `env`, `ports`. `name` required; `image` required unless a `templateId` supplies it. |
| GPU host constraints | `gpu.minCudaVersion` or `gpu.allowedCudaVersions`; `gpu.minRamPerGpu` and `gpu.minVcpuCountPerGpu`. The CUDA set and floor are mutually exclusive except an explicit empty set. |
| CPU create | One flavor ID from the CPU catalog and a power-of-two vCPU count, minimum 2. RAM is derived from the flavor. No persistent host-local mount on CPU Pods. |
| Storage | `mounts.persistent:{size,path}`, `mounts.network:[{volumeId,path}]`, `mounts.global:[{volumeId,path}]`. Persistent is incompatible with either external volume kind. Network/global permit at most one each. Paths required. Persistent size minimum 10 GB. |
| Pod response | `status`, `cost`, `startedAt`, nested GPU/CPU allocated resources, `dataCenterId`, `cudaVersion`, `runtime.uptime`, `runtime.ports`, `ssh.proxy`, `ssh.direct`. List responses omit live runtime/direct SSH details. |
| Pod lifecycle | `POST /pods/{id}/action` with `action:start\|stop\|restart\|terminate`; delete remains `DELETE /pods/{id}`. Valid actions are supplied in the Pod's `actions`. No reset action. Updates use PATCH. |
| Lists | Wrapped resource arrays. Pods and network volumes include required cursor pagination; follow `nextCursor` while `hasNextPage`, preserving opaque cursor values. Offset and Pod `includeMachine`-style flags have no v2 equivalent. |
| Network volumes | `/network-volumes`; `dataCenter` replaces `dataCenterId`; optional immutable `type:STANDARD\|HIGH_PERFORMANCE`. |
| Registry credentials | `/registries`; create/list/get/delete. Responses contain ID/name, not username/password. Container config refers to credential ID via `registry`. |
| Secrets | `/account/secrets`; values remain write-only. Name immutable; PATCH updates value and/or description. |
| Errors | RFC 9457 problem objects: `title`, numeric `status`, `detail`, optional `errors:[]`. Keep transport status authoritative. |

## Inventory and billing semantics

GPU/CPU/datacenter discovery now has dedicated `/catalog/...` routes.
For stock, explicitly request `include=AVAILABILITY&product=POD`.
GPU availability accepts count, cloud, country, and CUDA constraints;
`cloud` defaults to SECURE, so both clouds require separate scoped reads.
Availability levels are `NONE`, `LOW`, `MEDIUM`, `HIGH`; omitted availability
without an expansion is unknown, not empty stock. GPU `maxCount` is the largest
machine's GPU count, not available inventory. GPU prices are **per GPU per hour**;
CPU catalog prices are **per vCPU per hour**, and CPU RAM is `ramGbPerVcpu` times
the selected count. V2 does not expose the old exact host resource quote through
these catalog objects. Source: [live catalog schemas and parameters](https://api.runpod.io/v2/openapi.json).

`GET /billing/pods` returns `{records,metadata}`. Records have `podId`,
`startTime`, `endTime`, `totalAmount`, `gpuAmount`, `cpuAmount`, `diskAmount`.
There is no `timeBilledMs` or `grouping` query parameter. Metadata echoes the
resolved query, record/Pod counts, and totals. Query bounds snap outward to bucket
boundaries; preserve that echo with exact response bytes. Convert provider money
from JSON decimals, never through float64. Source:
[billing schema and endpoint](https://api.runpod.io/v2/openapi.json).

Management REST v2 exposes per-user `RateLimit` windows and `Retry-After`;
Serverless inference has separate per-endpoint limits. A create POST timeout may
have created a Pod; reconcile by listing rather than blindly retrying.
Source: [rate limits](https://docs.runpod.io/api-reference-v2/rate-limits).

## Capabilities without a v2 replacement

The checked v2 schema has no spot/interruptible bid input, minimum spot bid price,
account balance read, download/upload bandwidth placement floors, host machine ID,
latest container telemetry, or container exit code. It also cannot supply two
independent REST/GraphQL snapshots or the old GraphQL alias batching protocol.
These are absences in the checked contract, not claims that RunPod cannot add them.
Do not silently substitute on-demand purchases, zero balance/cost, invented
telemetry, or a different placement constraint. Delete unsupported API surfaces
or make their lack explicit in consumer migration. Sources:
[live v2 schema](https://api.runpod.io/v2/openapi.json), corroborating
[official CLI's remaining GraphQL uses](https://github.com/runpod/runpodctl/blob/main/README.md#environment-variables).

TensorHub must adopt the new SDK and adjust any removed capability before this
change is considered deployed. Durable prepared create records from legacy
transports require explicit reconciliation; they must not be replayed as a
changed purchase request under v2.

## SDK migration choices

The migration uses one management base (`WithBaseURL`) and native v2 Pod create
and response models. GraphQL transport/options, legacy REST paths, spot/bid
operations, balance reads, unsupported inventory resource filters, and dual-source
readback/coherence models are removed. Serverless inference retains its separate
base because it serves jobs rather than infrastructure management.

Normalized GPU/CPU offers retain exact aggregate hourly rates, with
`PriceSource=catalog_list_price`; their rates are derived from the cloud's
per-GPU or per-vCPU catalog rate. A selected datacenter scopes availability, not
the price. Datacenter storage support derives from `networkVolumeTypes`, and
normalized availability rows preserve the cloud that supplied them. Inventory
uses sequential REST reads, caches identical URLs within one pass, preserves
individual failures, and stops issuing requests after a rate limit or cancellation.
These are implementation decisions; full-check, consumer adoption and deployment
receipts must be recorded separately.

## Runtime observations, 2026-10-29

Normal SDK reads of the real v2 service exposed a catalog sentinel with `id` and `name` equal to `unknown`, explicit `memory: 0`, neither cloud supported, and `availability: NONE`. The v2 schema allows integer memory without a positive minimum. SDK0.5.1 preserves explicit zero metadata while still rejecting omitted, null or negative memory; stock and price checks remain independent.

An observation also contained global GPU availability without the optional per-datacenter detail, followed by a later observation with explicit locations. The datacenter census projects only explicit, cloud-scoped location rows and does not infer availability or absence for unlocated GPU types. A request for an exact datacenter quote still refuses positive global stock without location detail. This preserves useful independent rows without inventing placement evidence.

These were read-only SDK requests using the configured production credential in process memory. Credential values and full resource responses were not logged or persisted; no synthetic paid rental was created.
