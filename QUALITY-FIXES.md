# Benchmark quality remediation

Working branch: `feature/adversarial-suite`, based on `7d4587c`.
No client performance or security results are implied by harness unit tests.
No generated archives are committed. No new third-party dependencies are used.

## Performance review checklist

- [x] Human-readable experiment/fixture names with stable IDs; writer versions distinguish otherwise identical-looking RAR4 rows.
- [x] Reject duplicate fixture identities in historical phase comparisons and duplicate names in the generated catalog.
- [x] Reconcile supplied results with every planned run, including absent fixtures.
- [x] Require exact repetition ranges.
- [x] Snapshot workload manifests and submitted NZB hashes in results.
- [x] Revalidate saved sequential results using the runtime contract.
- [x] Reject different workload and shaper identities within a paired stratum.
- [x] Retain monotonic elapsed timing and request-start observation lower bounds.
- [x] Record native timeout DNFs; exempt unsuccessful runs from precision gates.
- [x] Preserve nested output topology and reject undeclared retained output.
- [x] Read Docker CPU outside the measured container; exclude perf teardown.
- [x] Replace Windows nominal-frequency conversion with OS user+kernel time.
- [x] Create Windows processes suspended, attach accounting, then resume.
- [x] Check process exit before final Windows accounting.
- [x] Explicit resource-window/collector compatibility and retained per-job perf evidence.
- [x] Acknowledged perf enable/disable; reject multiplexed/missing coverage, duplicate instruction records and nonfinite counter timing.
- [x] Queue-drain client timing separate from harness verification/cleanup.
- [x] Native entry/bundle/source/helper/declared-runtime inventory and normalized effective environment; bound symlink targets and retained inventory digest.
- [ ] Complete automatic native runtime closure discovery beyond declared roots (dynamically loaded system libraries remain host provenance).
- [x] Expanded host provenance, including OS/CPU/power facts where available.
- [ ] Automated capacity calibration and measured collector precision.
- [x] Exact poster-input segmentation attestation, bound to manifest/NZB and snapshotted in results (not independent wire readback).
- [x] Read-only pilot sizing: fastest-client duration lower bounds, common power-of-two payload tiers, failure/size-ceiling handling and source observation digest.
- [ ] Automated duration-tier generation and independent-session aggregation/policy enforcement (pilot separation and sizing policy are documented).
- [ ] Cache/power/host-load policy and observations.
- [ ] Memory/I/O and connection/pipeline workload axes.
- [x] Withhold class aggregates containing DNFs; retain conditional pair results.
- [x] Explicit coverage-balanced aggregate naming; no claimed posting-frequency weighting.
- [x] Clean/repair/encryption subgroup aggregates retaining DNFs and coverage-balanced weighting.
- [x] Reject client/helper build or TLS-policy drift across fixtures contributing to an aggregate.
- [x] Timing-sensitivity/practical-significance reporting; short runs marked descriptive-only.

## Adversarial review checklist

- [x] Human-readable, unique names for all generated cases.
- [x] Propagate output safety violations into security verdicts.
- [x] Inspect containment-case output safety and rejection postconditions.
- [x] Reject unknown check names; validate resource values despite absent evidence.
- [x] Unsupported is inconclusive, never a protection pass.
- [x] Run-bound typed/hashed evidence contract; capability gaps fail inconclusive.
- [ ] Platform evidence producers and instrumented detector canary self-tests.
- [x] Strict CLI comparison gate and exit status for incomplete observations.
- [x] Semantic recipe contracts, prerequisites, invariants and declared stage/coverage limits.
- [x] Refresh unrelated NZB sizes and uuencode envelopes after article mutation.
- [x] Ordered multipart control; reverse-order fixture retains original numbers.
- [x] Isolate NZB-path and yEnc-path naming layers.
- [x] Complete surrounding multipart data for targeted yEnc mutations.
- [ ] Valid compressed/encrypted/solid/multivolume external-writer seeds.
- [x] Precise recipe names instead of overstated semantics.
- [x] Independent multi-slice/multi-file PAR2 algebra and sufficient/surplus/insufficient/dependent variants.
- [x] Three additional PAR2 length/hash lies with consistent File IDs, Main/IFSC references, set IDs and packet hashes.
- [x] Distinct NZB file/segment floods and yEnc decoded-size boundary cases.
- [x] Responder lifecycle, bounded response transcript, run-bound fault-delivery evidence and conforming/nonconforming 400 closure.
- [x] Exact wire/fragmentation/transcript regressions, including retry semantics, missing-terminator lifetime and failed writes.
- [x] Count only delivered faults: ordinary article responses cannot attest a capabilities fault, successful retries cannot inflate first-failure counts, and delivered slow-response delays survive cancellation.
- [x] Authoritative plans with required controls and deterministic repetitions.
- [x] Portable recorded verdicts, independent of surviving output directories.
- [ ] Complete software/observer provenance and component adapters.
- [x] Exploratory Weaver CLI adapter with exact version/binary identity, bounded logs, output verification, low-scratch guard and fail-closed Linux isolation checks.
- [x] SABnzbd one-shot adapter with exact image/version identity, fresh per-case state, API submission/history polling and output verification.
- [x] Explicit control-backed capability exclusions and bounded-stall adjudication that cannot suppress secondary safety failures.
- [x] Batched, depth-limited output enumeration and cumulative read budgets.
- [x] Opened-handle identity/type checks and privileged-mode rejection.
- [x] Exact bundle inventories and reserved, staged atomic export.
- [x] Fixed escape and command-execution canaries checked after every supervised case.
- [x] Symlink/hardlink pivots, replacement chains, nested confusion and concurrent cross-archive collisions.
- [x] Duplicate-key/depth/size-safe evidence JSON decoder.
- [x] Explicit termination presence and nonblank client/tool identity validation.
- [x] Bounded independent extractor/repair oracles, including nonzero-exit classification.
- [ ] Platform detector efficacy and expanded CLI scoring tests.
- [ ] Seeded mutation and generable minimization workflow.
- [x] Automated tracked-file extension/magic guard against checked-in archives and binary fixtures.
- [ ] Additional XML/assembly/archive/PAR2/filesystem/protocol/lifecycle families.

## Validation

The final normal and race-enabled sweeps each passed 1,330 tests across 21
packages. `go vet ./...` passed. Linux amd64 and Windows amd64 source builds
passed; cross-compiled target binaries were not executed. The optional oracle
sweep passed 773 tests, including the installed `7zz` and `par2` assertions.
All original 536 recipe-v3 cases passed deterministic
generation/export/verification, and a supervised Weaver 0.11.3 Linux campaign
produced a latest result for every case. A supervised, digest-pinned SABnzbd
5.1.2 campaign also produced exactly those original 536 unique results: 31 exact
outputs verified, 14 control-backed capability exclusions, 13 explicitly
bounded stalls and four review-required cases representing three private
finding classes. The final version-explicit adapters passed isolated
exact-output control smoke tests.

The subsequent Weaver-only expansion added 75 unique cases, bringing the
catalog to 611. All 75 were exercised against digest-pinned Weaver 0.11.3: 15
exploit-chain cases, 56 expanded path cases and four cross-archive race cases.
The race cases were repeated 25 times each, for 171 observations total. Neither
fixed canary changed and no review-required result was produced. These additions
were not submitted to SABnzbd. The shared-namespace limitation still makes the
security result inconclusive rather than a proof of safety.

Three generable regressions derived from published SABnzbd PAR2 advisories then
brought the catalog to 614 and were exercised only against the same pinned
Weaver binary. The direct CVE-2021-29488 case reached PAR2 filename placement;
Weaver encoded the parent-directory component and kept the result in the job
root. The two archive-nested PAR2 cases completed without a canary change, but
Weaver 0.11.3 treats extracted PAR2 files as outputs rather than new repair
inputs. Its ZIP extractor also materialized the advisory's symlink entry as a
regular one-byte file. Those two results are evidence that the historical chains
were unreachable through this pipeline, not evidence that their PAR2 placement
sinks were exercised. All three security verdicts remain inconclusive and none
was submitted to SABnzbd.

Five fresh-instance API probes inspired by the six product-specific advisories
also ran against pinned Weaver 0.11.3. Authentication controls, explicit-key
precedence, mode-confusion requests, a `__wrapped__` path, and logout cookie
expiry behaved as expected, with no potential finding. The missing SAB orphan
management analogue and intended administrator-controlled script execution are
documented as separate trust boundaries rather than counted as fixture passes.

The supervised adapter shares its namespace with the client and intentionally
reports security as inconclusive; it is not a substitute for the unchecked
independent platform evidence producers above. Remaining unchecked work is not
claimed complete. Windows runtime, Linux performance, cross-client campaigns
and independent security observers still require their named execution
environments; compilation or diagnostic containment is not a security pass.
