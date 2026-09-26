Owner: /root/h3_reference_astra
Purpose: preserve an open-ended CUDA host floor and quote/create network constraints through exact-byte prepared GPU creation.
Branch: fix/cuda-floor-prepared-create-20260926
Base: 77d4fd1 (fresh origin/master, 2026-09-26)

Primary evidence: RunPod GraphQL schema supports minCudaVersion plus network/public-IP constraints on podFindAndDeployOnDemand. REST v1 documents no Pod CUDA floor; REST v2 omits current network/public-IP placement constraints. New floor requests therefore use a closed prepared GraphQL mutation; existing REST bodies retain their original transport on replay.

Validation scope: source review only. User prohibits tests and CI. No provider calls or deployment by this task.
