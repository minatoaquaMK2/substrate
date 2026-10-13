# Draft dependency discussion: strict private-memory CoW restore contract

**Independent Cloud Hypervisor contribution draft, not submitted upstream.** Production
Substrate shared-memfd/virtiofs CoW remains **dependency-blocked**; guest hardware
validation remains **hardware-blocked**.

## Problem and bounded implementation

Pinned CH v53 lacks CopyOnWrite. Inspected CH upstream `c7dd43dbc3b625a7a21b9cb84d4a49b1663e902c` already implements private
CoW but may copy shared/incompatible memory layouts. A successful restore API
response alone therefore cannot prove a requested strict CoW mode occurred.
The local v53 patch implements a real private file mapping restore subset,
validates all saved ranges before mapping, and advertises a strict capability.
Explicitly incompatible layouts reject instead of silently copying.

This is not a claim to have invented upstream private CoW or to have solved
shared vhost-user backing. Before proposing a patch, rebase the useful strict
contract/range-validation/tests onto current upstream and discuss whether that
contract is wanted. A backport for reproducible local experiments is not an
appropriate standalone novelty claim.

## Safety and test boundary

The supported subset excludes shared/file-backed/hugepage/THP/zone/hotplug and
external shared-memory device layouts. Dirty private bytes continue through the
existing complete checkpoint serialization path. Source tests exercise actual
production mapping helpers, A/B isolation, source lifetime and repeated dirty
snapshots. Build/source checks do not substitute for guest validation.

See [dependency instructions](../dependencies/README.md) for exact pins, patch,
build commands and bounded KVM harness. No `/dev/kvm` was available; normal
missing-prerequisite and required-failure branches were exercised in the
first-delivery run. The fresh reconstruction uses no-KVM harness regressions;
it did not rerun real prerequisite probes or restore a guest. Maintain default Substrate asset pins; experimental binaries
must have reviewed source provenance and verified digests.

## Contribution and legal disclosure

The patch and draft were AI-assisted. Human review, understanding, final text
and any repository-required DCO/AI disclosure are mandatory before submission.
No DCO, CLA or other legal signature was made. No upstream issue, PR or dependency-fork push was
sent. Checkpoint commits are local reproducibility records, not upstream-ready
commits. Substrate-specific collaboration rules and CH's current contribution
rules both need human review for their respective submissions.
