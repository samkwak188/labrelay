# LabRelay

LabRelay snapshots completed datasets, resumes interrupted tus uploads, independently verifies exact S3 object versions, and publishes an immutable dataset revision in PostgreSQL. It never treats transfer completion as publication.

**Status: implemented local release candidate, not a completed cloud pilot.** The implementation has run against real PostgreSQL 18 and versioned SeaweedFS in WSL2. See [validation evidence](docs/validation.md) for measured results and remaining acceptance gates. Real AWS acceptance, a fresh cloud-host restore, and the 14-day independent-user trial remain outstanding.

**Current environment blocker:** C: filled during the final benchmark. About 805 MB of generated provider cache was reclaimed, but WSL could not restart. Source and evidence are preserved. Final regression checks and release rebuild must run after the host recovers; older `dist/` packages are explicitly marked **do not release**.

## Run locally

Requirements: Linux or WSL2, Go **1.27.1**, Docker Engine with Compose, Python 3 for test/benchmark tools. Keep the spool on a Linux filesystem. The repository may live on a Windows mount; the spool may not.

For WSL development, keep at least 12 GiB free on the Windows backing disk as well as adequate Linux space. The local scripts now check C: before larger builds/tests because WSL's virtual filesystem can report ample space while Windows is full. If the distribution is stored elsewhere, check that backing drive too.

From the repository in a Linux shell:

```bash
bash scripts/dev.sh
```

This starts pinned PostgreSQL/SeaweedFS containers, builds both binaries, migrates the catalog, enables bucket versioning, creates a development token, and serves on `127.0.0.1:18080`. In another Linux shell:

```bash
source scripts/local-env.sh
export LABRELAY_TOKEN="$(cat "${XDG_RUNTIME_DIR:-/tmp}/labrelay-dev-token-$(id -u)")"
bin/labrelay doctor
mkdir -p /tmp/labrelay-example
printf 'completed experiment\n' > /tmp/labrelay-example/results.txt
bin/labrelay prepare --root /tmp/labrelay-example --name example --dry-run
bin/labrelay prepare --root /tmp/labrelay-example --name example
bin/labrelay sync
bin/labrelay list
bin/labrelay fetch REVISION_ID --dest /tmp/labrelay-downloaded-example
```

Use a **new** fetch destination. All files must verify for success. A failed fetch retains verified files and a private `.labrelay-incomplete-*` staging directory for diagnosis; retry into a new directory. No original source file is deleted or modified.

All operational commands currently emit JSON; `--json` is accepted explicitly. `status BUNDLE_ID` includes remote state when credentials are available. `sync --watch` runs until interrupted; ordinary `sync` has a bounded retry budget. `sync --retry BUNDLE_ID` explicitly permits replacing a failed/expired remote session. `prune` removes only snapshots published at least 24 hours earlier and retains receipts. Dry-run validates structure, expected sizes and space; full byte hashing happens during actual preparation.

Configuration uses environment variables:

Each spool is bound to its first upload server/project. Use separate spool directories for different destinations; a local published receipt must never silently stand in for publication to a different project.

| Variable | Purpose/default |
|---|---|
| `LABRELAY_SPOOL` | `~/.local/state/labrelay`, Linux filesystem |
| `LABRELAY_SERVER` | HTTP origin, default `http://127.0.0.1:8080` |
| `LABRELAY_PROJECT` | Project token scope, default `pilot` |
| `LABRELAY_TOKEN` | Individual bearer token |
| `DATABASE_URL` | Server PostgreSQL DSN |
| `S3_BUCKET`, `AWS_REGION` | Private versioned bucket and region |
| `S3_ENDPOINT` | Local compatibility endpoint; omit for AWS |
| `LABRELAY_LISTEN` | Server listener, default `127.0.0.1:8080` |
| `LABRELAY_TEMP` | Tus multipart temporary directory, default `/tmp/labrelay-upload` |

Development credentials in `scripts/local-env.sh` are for loopback-only containers. Production uses an EC2 instance role, private S3, and individually generated application tokens.

## Import CATS

```bash
bin/labrelay import cats --root /path/to/complete/CATS-checkout \
  --scene data/scenes/chiangmai_intersection.json \
  --frames data/frames/chiangmai_intersection/manifest.json
```

The Chiang Mai bundle requires 33 artifacts: original video, 29 frames, scene card, JSON manifest, and CSV manifest. A metadata-only checkout is insufficient. The importer checks video references, frame-path joins, counts, sizes and hashes, and preserves provenance artifacts byte for byte. Historical paths and commands in provenance are never executed or followed. Successful transfer says nothing about calibration accuracy.

## Verify and operate

Stop a development daemon before running server integration/fault tests: the catalog permits one server process.

```bash
bash scripts/check.sh                  # format, vet, unit, race, fuzz smoke
bash scripts/test.sh integration       # real local PostgreSQL + S3
bash scripts/fault-test.sh             # actual process deaths and response loss
bash scripts/restore-drill.sh          # NEW local database, exact-version checks
bash scripts/benchmark.sh              # 10 paired measurements + warm-up
bash scripts/release.sh 0.1.0-rc.2      # Linux amd64/arm64 archives + SHA-256
```

Raw operational outputs go to `results/local/` and are excluded from Git. Selected evidence belongs in `evidence/`, with its environment and limitations. The release script produces `dist/`; it does not publish to GitHub or provision AWS.

- [Architecture, invariants and limits](docs/architecture.md)
- [OpenAPI contract](api/openapi.yaml)
- [Deployment, upgrade, backup and restore](docs/runbook.md)
- [Validation and remaining gates](docs/validation.md)
- [Benchmarks and existing-tool comparison](docs/evaluation.md)
- [Independent-user trial](docs/pilot.md)

The supported applications are `labrelay` and `labrelayd`. `tools/tus-baseline` is an isolated measurement fixture, not a supported service.
