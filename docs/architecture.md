# Architecture and invariants

The collector writes immutable snapshots before queue registration. The server writes objects before catalog publication. Publication is one PostgreSQL transaction; there is no distributed transaction with S3.

```mermaid
flowchart LR
  Source[Completed source files] --> Snapshot[Copy + SHA-256 + fsync]
  Snapshot --> Spool[Private snapshot + SQLite WAL/FULL]
  Spool --> Tus[Authenticated HEAD/PATCH, embedded tusd]
  Tus --> S3[Private versioned S3]
  S3 --> Verify[Read exact version and hash all bytes]
  Verify --> PG[Lock ingestion and publish atomically]
  PG --> Receipt[Immutable manifest + verified version tuples]
  Receipt --> Fetch[Rooted download and independent hash check]
```

## Identity and publication

The collector emits sorted compact JSON followed by a newline. Identity is `(project, dataset name, SHA-256 of the exact manifest bytes)`. The server preserves those bytes. API state returns them as base64 `manifest_bytes`; decoding and hashing reconstructs identity without JSON reserialization.

The catalog stores expected paths, sizes and hashes separately for enforcement. A published receipt includes bucket, key, version ID, expected/verified size and SHA-256 for every file. The bucket is bound in catalog settings and cannot be changed by configuration. Object keys come from random server attempt IDs. Path metadata never selects storage keys.

States: `STAGING → VERIFYING → READY`; `FAILED` and `EXPIRED` are unsuccessful terminal sessions. Transient failures retain state. Ordinary duplicate registration returns the existing live session or receipt. Explicit `?retry=true` may create a new session after failure/expiration, preserving logical identity.

The publication transaction locks the ingestion, checks the deadline and requested state, checks every file's verified tuple, creates a unique revision ID, records the event, and commits `READY`. Expiration takes the same lock. Ready ingestions retain their identity uniqueness and are never expired. Repeated finalization returns the same receipt.

## Source and spool

Sources must be completed regular files. Generic preparation rejects symlinks, unsafe paths and special files. Preparation compares file size and modification time around copying and hashes the copied bytes. It is not an atomic snapshot of a live producer. No hardlinks to sources are used.

Files, manifest and directories are synchronized before renaming a snapshot into `snapshots/<digest>`. SQLite registration follows rename. Startup recovers completed orphan snapshots; interrupted temporary snapshots never enter the upload queue. Process locks serialize preparation and syncing. SQLite uses WAL, `synchronous=FULL`, one connection, and a busy timeout. Receipt and retry state survive process restart. Local offsets are informational; remote tus HEAD is authoritative.

Private spool permissions and read-only snapshot files reduce accidental edits. They do not defend against the owner's deliberate modification. The collector rehashes a snapshot before upload; the server independently checks uploaded bytes.

## Transfers and recovery

Creation commits an attempt in `CREATING`, calls the tus S3 storage API with a fresh object identity, then saves tusd's **actual composite upload ID** and commits `ACTIVE` before exposing a URL. A lost committed response is recovered through the application API. Exclusive startup tombstones unexposed attempts; the worker also handles timed-out creation reservations. Replacement attempts always use fresh object keys.

Public tus creation, GET, DELETE, concatenation and deferred-length paths are inaccessible through the application's HEAD/PATCH-only router. Each request looks up the attempt and token, takes the per-attempt gate, and rechecks state/deadline. Tusd supplies its own in-process upload lock. The collector reconstructs `tusgo.Upload`, calls `Sync`, and seeks to the server offset after uncertain results.

Notifications wake verification. A five-second reconciler discovers completed S3 objects independently of notifications. It obtains version and length, hashes that exact version, compares the complete count/hash, then persists the tuple. Multipart ETags are never used as content identity.

One process holds a PostgreSQL session advisory lock. Loss of that session closes admission and exits the daemon; the process supervisor restarts it. Deployments must stop the old process before starting a new one. This is a single-instance pilot design, not a distributed fencing or high-availability protocol.

## Cleanup and downloads

Unsuccessful attempts are tombstoned first. The worker acquires an exclusive attempt gate, drains in-flight requests, rechecks the catalog and removes only the exact attempt key, its `.info`/`.part` support keys, their versions and its multipart uploads. Published attempts are excluded. Catalog-unknown objects are reported by `audit-storage`, never automatically removed.

Fetch creates a new destination via rooted filesystem operations. It verifies the exact manifest and file set, streams each catalog-pinned version into private temporary files, hashes/synchronizes them, and atomically links each verified file into place with no-overwrite semantics. This is the deliberate Linux equivalent of a no-replace rename. An incomplete directory cannot report success.

## Fixed limits

| Limit | Value |
|---|---:|
| File / dataset | 2 GiB / 5 GiB |
| Files / manifest | 5,000 / 2 MiB |
| Spool / free-space reserve | 20 GiB / 1 GiB |
| Collector parallel files | 2 |
| PATCH / S3 preferred part | 8 MiB / 8 MiB |
| Concurrent PATCH requests | 4 |
| Verification workers | 1 |
| Ingestion lifetime | 7 days |
| Published local retention | 24 hours |
| Catalog dataset reservation | 50 GiB, global pilot ceiling |

Reservation admission is serialized in PostgreSQL. Published datasets continue consuming reservation. The 50 GiB limit covers declared dataset bytes, not temporary multipart/support versions, logs, catalog growth or backups. It is not an AWS spending cap. Limits are code defaults in this candidate; changing them requires a reviewed build.

## Boundaries

No live recording, directory watching, browser uploads, model execution, global deduplication, multi-replica service or published-data deletion. API transport is loopback/SSH for the pilot; use TLS before widening exposure. Tokens are random, individually revocable, project scoped and stored as SHA-256 hashes. Logs and metrics contain no bearer credentials. Dataset/file labels are excluded from metrics.
