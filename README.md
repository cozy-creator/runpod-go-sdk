# runpod-go-sdk — retired

**This repository is retired and archived as a public, read-only reference. For new integrations, use the [official Runpod Go SDK](https://github.com/runpod/go-sdk).**

## Official replacement

For Runpod REST v2 resource management:

```bash
go get github.com/runpod/go-sdk/v2
```

Follow the [official documentation and examples](https://github.com/runpod/go-sdk#readme). Use its v2 client rather than its legacy compatibility package.

For Serverless job invocation (`run`, `runsync`, status, stream, cancel and health), Runpod maintains the separate [`github.com/runpod/go-sdk/pkg/sdk`](https://github.com/runpod/go-sdk/tree/main/pkg/sdk) module. The management and inference APIs are separate services.

## Why this project existed, and why it is retired

The official repository dates to 2023, when its original client served Serverless jobs. This project [already implemented Pod management in June 2025](https://github.com/cozy-creator/runpod-go-sdk/commit/31db935). The official generated REST v2 management client was [added on October 13, 2026](https://github.com/runpod/go-sdk/commit/d7cd5e28594e190ada4564133bb7d725faa1d617) and now covers more management API areas.

Maintaining another complete API client is no longer the direction of this project. Use the official client and place application-specific guarantees in a consumer adapter. For TensorHub, those contracts belong in its existing provider layer:

- Exact decimal money decoding and qualified billing evidence, including bounded responses and identity/window/totals validation.
- Persisted purchase bytes, typed capacity failures, and reconciliation after an ambiguous create.
- Retained quota cooldowns and complete, validated resource observations.

The official client already protects POST/PATCH against ambiguous transport/server-error retries and provides raw request and response access. Its generated money fields use floating-point types, paginated operations return one page, and retry state is per request; consumers needing the contracts above must preserve them explicitly. This is a dated comparison against [official v2.1.0](https://github.com/runpod/go-sdk/tree/d261aaf1f3cb4cd73c4c73fef1631e6d5a222958), checked October 29, 2026.

## Existing consumers

This is **not a drop-in import replacement**. Archiving preserves this repository’s history, tags, and [v0.5.1 release](https://github.com/cozy-creator/runpod-go-sdk/releases/tag/v0.5.1) for pinned consumers.

TensorHub currently pins v0.5.1. Replacing that dependency with the official client and a qualified provider adapter is a separate migration; archiving this repository does not perform it.

The last maintained SDK used REST v2 only for management, with separate Serverless inference support. Historical usage documentation is retained in [the v0.5.1 reference](docs/archived-sdk-v0.5.1.md); the [v2 migration notes](docs/rest-v2-migration-20261028.md) record the contract and live-read findings.

## License

MIT. See [LICENSE](LICENSE).
