# Validation and release status

LabRelay is an implemented local candidate, **not a completed cloud/peer pilot**. The original workstation's final validation was interrupted when its Windows C: drive filled. Removing the generated Terraform provider cache recovered about 805 MB, but Ubuntu WSL still failed to start and Docker commands became unresponsive. No source, original dataset, catalog or published object was deleted to free space. GitHub CI subsequently completed regression validation of the committed implementation.

## Verified in GitHub CI

[CI run 37163282788](https://github.com/samkwak188/labrelay/actions/runs/37163282788) passed for commit `59702323164fb3887309f9059821aaa0b48de9a4` on Ubuntu 24.04. It ran formatting, vet, unit and race checks, both manifest/path fuzz smoke tests, ten AWS-harness tests using mocks/Bash, real local PostgreSQL/S3-compatible integration, all eight separate process-death/network-loss fault scenarios, the expanded restore drill, and Terraform format/locked-provider validation.

The restore drill checked nine published revisions and eleven files in a new database. Missing, empty and `null` version IDs, a hash mismatch and a size mismatch each caused validation to fail while retaining maintenance mode. Repairing the cloned references allowed full validation to reopen the clone. Selected raw outputs, source identity and scope are committed in [`evidence/ci-37163282788/`](../evidence/ci-37163282788/).

The original [CI run 37096331456](https://github.com/samkwak188/labrelay/actions/runs/37096331456) also passed for `7b838b5`, validating the final spool inventory/destination binding and publication regression-test setup changes. These CI runs do not establish real AWS behavior, a completed benchmark, or a cloud deployment.

## Rebuilt release candidate

[Release run 37163327369](https://github.com/samkwak188/labrelay/actions/runs/37163327369) passed for `59702323164fb3887309f9059821aaa0b48de9a4`. It reran `scripts/check.sh`, rebuilt both Linux architectures as `0.1.0-rc.2`, generated SHA-256 checksums, and built the container. The downloaded archives matched their checksum file and contained executable ELF binaries for the expected architectures plus the README. [Recorded results and checksums](../evidence/release-37163327369/) identify these new packages; the original workstation's stale packages remain unsuitable for release.

The archive binaries were inspected on Windows, not executed there. The workflow did not publish the container to a registry or publish a GitHub Release. Cloud and independent-user gates still apply.

## Verified locally before the host failure

The environment was Ubuntu under WSL2, Go 1.27.1, PostgreSQL 18.0 and the pinned SeaweedFS image. Spools lived on Linux storage. The local tests used real databases/object storage and separate collector/server processes where indicated.

| Check | Observed result |
|---|---|
| Formatting, vet, unit, race and fuzz smoke | Passed in `checks-20261002T203954Z` |
| Snapshot process exits | Partial snapshots stayed out of the queue; completed orphan snapshots recovered |
| CATS source | All 33 real artifacts, 107,490,063 bytes, prepared, published and fetched |
| Many-file workload | All 5,000 files, 20,480,000 data bytes, prepared, published and fetched |
| Empty and multipart files | Empty, small and 8 MiB+19-byte files verified correctly |
| Version pinning | Replacing an object with a newer version did not change fetched published bytes |
| Creation crashes | Reservation, storage creation and committed-ID failures recovered |
| Publication crashes | Before/after commit failures returned one stable published receipt |
| Real SIGKILL | Restart retained the acknowledged 8 MiB offset |
| Network proxy | Lost creation response, truncated PATCH body and lost accepted PATCH response recovered; fetched bytes verified |
| Corruption and authorization | Corrupt bytes did not publish; invalid/foreign/revoked tokens denied, including HEAD |
| Expiration and cleanup | Expired upload rejected and cleaned; published versions survived |
| Deadline lock race | Regression test failed before the fix and passed after deadline checking moved after lock acquisition |
| Actual dependency outages | Local PostgreSQL and S3 services stopped/restarted; no false success and durable recovery |
| Restore | A dump restored into a NEW local database; nine published revisions' exact versions checked before reopening |
| Infrastructure | Terraform 1.16.5 and locked AWS provider validated; no AWS resources applied |
| Packaging | Earlier source revision built locally; the subsequent CI rebuild is recorded above |

Selected raw evidence is in [`evidence/`](../evidence/); complete development logs remain under ignored `results/local/`. The original local evidence predates Git initialization; source fingerprints identify that saved code. The CI result above identifies the tested Git commit.

The last full passing workstation suite preceded the final spool inventory/destination-binding change and the fresh-database setup change in the publication regression test. **The updated application binaries compiled and ran six measured corrected benchmark pairs successfully** before disk exhaustion. The subsequent GitHub CI run passed the complete regression workflow for those changes; the corrected ten-pair benchmark remains incomplete.

A host-space preflight was added after the incident to the local build/test/benchmark entry points. CI exercised its Linux checks; its WSL backing-disk check still needs execution on a recovered WSL host.

## Defects found and fixed

- SQLite nullable receipts required scanning into bytes before assigning the JSON raw-message value.
- Tus completion notifications require an independent drain to avoid waiting on the verifier's request gate.
- Manifest identity requires exact bytes; API responses carry base64 `manifest_bytes`, preserving whitespace.
- Losing the exclusive PostgreSQL connection now exits the daemon for supervisor restart.
- Wall-clock upload timing jumped under WSL; durations now use monotonic elapsed time.
- PostgreSQL may evaluate SELECT expressions before waiting for a row lock. Publication checks the database clock in a separate statement after locking and guards its final state update.
- A spool now binds to its upload server/project so a receipt for one destination cannot suppress publication to another.

## Required gates still open

1. Repeat the corrected ten-pair benchmark on a working Linux host. On the original workstation, recover host free space and WSL/Docker first. Its `dist/DO-NOT-RELEASE.txt` identifies stale packages; fresh `0.1.0-rc.2` archives have now been rebuilt and checked as recorded above.
2. Run real AWS S3 multipart/version, process-crash and network-loss acceptance. The OIDC workflow exists; no AWS credentials/configuration were available here.
3. Test controlled mid-write collector/server ENOSPC on constrained Linux filesystems. The actual Windows backing-disk failure is **not** a passing controlled durability test.
4. Expand randomized cleanup/expiration/transfer races and concurrent registration by two independent collectors. The five deterministic restore failure injections above now pass locally in CI; real AWS execution remains open.
5. Run maximum 2 GiB boundary tests and final controlled ten-repetition measurements for all workloads, complete S3 request accounting, server peak-RSS sampling and a profiling-driven improvement.
6. Complete EC2 deployment, current cost estimate, delivered alarms, upgrade/rollback and a fresh-host restore. A local new-database drill does not establish the fresh-host target.
7. Complete fourteen days of operation, three peer datasets and an implemented usability improvement driven by that peer.

The portfolio claim currently supported is a Go implementation with **locally demonstrated** resumable ingestion, independent integrity verification, atomic catalog publication and process/network recovery. Cross-machine cloud operation and lab adoption remain unproven.
