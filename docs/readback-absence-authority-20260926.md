# Readback absence authority

Owner: /root/evolution_tolerance_audit
Branch: fix/readback-absence-authority-20260926
Base: 419e73dbbd573c6972b52572293dedde94f71d4a
Worktree: /home/fidika/cozy/.worktrees/runpod-go-sdk/readback-absence-authority-20260926

GetPodReadback first obtains the REST resource, then supplemental GraphQL facts.
A missing supplemental projection must not match ErrNotFound for the pod that
REST just found. Preserve Partial.REST and the original Cause for diagnosis while
classifying supplemental absence as incomplete readback. REST 404 remains actual
resource absence; authorization and network errors remain errors, not absence.

Implemented using the existing PodReadbackError: Partial and Cause retain the
primary observation and original provider diagnosis. Unwrapping supplemental
not-found returns ErrIncompleteReadback; other causes preserve their existing
classification. No new readback object or fallback API is introduced.

Validation passed: five real httptest/SDK classification cases (REST 404,
GraphQL null, GraphQL HTTP 404, GraphQL authorization failure, malformed GraphQL),
plus existing CPU coherence regression. Required repository go fmt, go vet and
go build passed with nice15/GOMAXPROCS2. No live provider calls or full local CI.
