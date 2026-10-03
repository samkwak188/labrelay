# Validation and release status

LabRelay is an implemented local candidate, **not a completed cloud/peer pilot**. Final validation was interrupted when the Windows host C: drive filled. Removing the generated Terraform provider cache recovered about 805 MB, but Ubuntu WSL still failed to start and Docker commands became unresponsive. No source, original dataset, catalog or published object was deleted to free space.

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
| Packaging | Container and Linux archives built at an earlier source revision; final rebuild is blocked |

Selected raw evidence is in [`evidence/`](../evidence/); complete development logs remain under ignored `results/local/`. The workspace has no fabricated Git commit ID. Source fingerprints identify the saved code.

The last full passing suite preceded the final spool inventory/destination-binding change and the fresh-database setup change in the publication regression test. **The updated application binaries compiled and ran six measured corrected benchmark pairs successfully**, but those final changes still need the complete checks rerun. Do not describe the current workspace as fully regression-tested.

A host-space preflight was added after the incident to the local build/test/benchmark entry points. Its execution also remains pending environment recovery.

## Defects found and fixed

- SQLite nullable receipts required scanning into bytes before assigning the JSON raw-message value.
- Tus completion notifications require an independent drain to avoid waiting on the verifier's request gate.
- Manifest identity requires exact bytes; API responses carry base64 `manifest_bytes`, preserving whitespace.
- Losing the exclusive PostgreSQL connection now exits the daemon for supervisor restart.
- Wall-clock upload timing jumped under WSL; durations now use monotonic elapsed time.
- PostgreSQL may evaluate SELECT expressions before waiting for a row lock. Publication checks the database clock in a separate statement after locking and guards its final state update.
- A spool now binds to its upload server/project so a receipt for one destination cannot suppress publication to another.

## Required gates still open

1. Restore adequate host free space, recover WSL/Docker, run `scripts/validate-local.sh`, repeat the corrected ten-pair benchmark, and rebuild release artifacts. `dist/DO-NOT-RELEASE.txt` identifies stale packages.
2. Run real AWS S3 multipart/version, process-crash and network-loss acceptance. The OIDC workflow exists; no AWS credentials/configuration were available here.
3. Test controlled mid-write collector/server ENOSPC on constrained Linux filesystems. The actual Windows backing-disk failure is **not** a passing controlled durability test.
4. Expand randomized cleanup/expiration/transfer races, concurrent registration by two independent collectors, and missing/corrupt-version restore failure injection. Existing checks cover narrower deterministic cases.
5. Run maximum 2 GiB boundary tests and final controlled ten-repetition measurements for all workloads, complete S3 request accounting, server peak-RSS sampling and a profiling-driven improvement.
6. Complete EC2 deployment, current cost estimate, delivered alarms, upgrade/rollback and a fresh-host restore. A local new-database drill does not establish the fresh-host target.
7. Complete fourteen days of operation, three peer datasets and an implemented usability improvement driven by that peer.

The portfolio claim currently supported is a Go implementation with **locally demonstrated** resumable ingestion, independent integrity verification, atomic catalog publication and process/network recovery. Cross-machine cloud operation and lab adoption remain unproven.
