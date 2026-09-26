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
