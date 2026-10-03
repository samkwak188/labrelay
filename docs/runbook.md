# Deployment and operations

This runbook is implemented tooling plus explicit operator steps. Terraform validates locally. It has not been applied to AWS in this workspace. Do not substitute local S3 compatibility results for real AWS acceptance.

## Before provisioning

1. Run the local checks, integration suite and fault suite. Build the release and container. Push that image to a registry you control and record its immutable digest; the supplied workflow builds artifacts but does not publish them.
2. Record a **dated current estimate** in `evidence/cloud-cost.json` using AWS's [EC2](https://aws.amazon.com/ec2/pricing/on-demand/), [EBS](https://aws.amazon.com/ebs/pricing/), [S3](https://aws.amazon.com/s3/pricing/) and [public IPv4](https://aws.amazon.com/vpc/pricing/) pricing. Include 730 instance hours, 50 GiB gp3, one public IPv4, dataset bytes and retained versions/support objects, requests, backups, logs, alarms and transfer. Target $30–50/month; budget alerts do not cap spending. No cost estimate is asserted in this candidate.
3. Configure an AWS profile with provisioning permissions. Use Terraform 1.16.5 and the committed provider lock file. Back up the Terraform state securely; it contains infrastructure details.
4. Set `ssh_cidr` to your address `/32`, `ssh_public_key` to a public key, and `alert_email`. Run `terraform -chdir=infra/terraform init`, `plan -out=tfplan`, inspect the concrete plan and estimate, then `apply tfplan`. Never place private keys or tokens in `.tfvars`.

The configuration creates one Ubuntu 24.04 `t3.small` with standard CPU credits, a 20 GiB root disk plus a separately retained 30 GiB data volume, SSH-only ingress, private encrypted S3, versioning, an instance role, CloudWatch logs/alarms and an account-wide budget alert. Dataset storage has **no expiration lifecycle**. The separate backup bucket expires catalog backups after seven days. SNS alert delivery requires the recipient to confirm the subscription.

## First host setup

Wait for cloud-init to finish: `sudo cloud-init status --wait`. Copy `deploy/` files to `/opt/labrelay`. Do not copy local development credentials. Use the `data_volume_id` output to identify the dedicated 30 GiB disk, inspect `lsblk`, then on the first blank volume:

```bash
sudo bash /opt/labrelay/mount-data.sh vol-REPLACE --initialize-empty
cd /opt/labrelay
sudo cp env.example .env
sudo chmod 600 .env
```

Edit `.env` with Terraform outputs, the application image digest and a random **hexadecimal** database password (avoids DSN escaping problems). Keep the data volume mounted at `/srv/labrelay`. `upgrade.sh` refuses to run otherwise. Set directory ownership for upload staging through that script; the application runs as UID 65532. PostgreSQL owns its data subtree.

```bash
sudo bash /opt/labrelay/upgrade.sh
sudo cp /opt/labrelay/labrelay-*.service /opt/labrelay/labrelay-*.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now labrelay-backup.timer labrelay-monitor.timer
```

Generate each person's token on the host:

```bash
sudo docker compose --env-file /opt/labrelay/.env -f /opt/labrelay/compose.yaml run --rm app token pilot write
```

Give the peer that token through an appropriate private channel. Use `read` for download-only access. The token is printed only at generation; the catalog stores its hash. Revoke by supplying `LABRELAY_TOKEN` to the `revoke` administrator command. Never paste tokens into issues, benchmark evidence or logs.

From each client, forward the local API:

```bash
ssh -L 18080:127.0.0.1:8080 ubuntu@HOST
export LABRELAY_SERVER=http://127.0.0.1:18080
export LABRELAY_PROJECT=pilot
export LABRELAY_TOKEN=YOUR_PRIVATE_TOKEN
labrelay doctor
```

The API remains bound to host loopback. The Docker bridge listener is internal; no public API security group rule exists.

## Upgrade and rollback

Take a catalog backup and record the current image digest. Edit `LABRELAY_IMAGE` to the new digest and run `upgrade.sh`. It pulls the image, stops the old daemon, starts/checks PostgreSQL, applies migration SQL, and starts the new daemon. Two daemon processes must never overlap. A catalog session advisory lock adds an admission check, but does not replace the stop/start procedure.

Migration 1 is the initial idempotent schema in `internal/server/schema.sql`, recorded in `schema_migrations`. Future versions must add ordered migrations and explicit compatibility tests; never edit an already-deployed migration's meaning. Roll back the **application image** by restoring the previous compatible digest and running the same stop/start procedure. Do not automatically reverse data migrations. If a migration is incompatible, restore a backup into a new catalog in maintenance mode instead.

## Backups and restore

`backup.sh` makes a consistent `pg_dump -Fc`, uploads it privately under a timestamped key and removes its local temporary file. The nightly timer provides an initial **24-hour RPO target**, not a verified availability guarantee. Confirm timer success and backup objects daily during the pilot.

For restore:

1. Stop `app`, disable its restart while restoring, and leave all S3 objects intact. Stop the monitoring timer if it would confuse the drill log. Retain the current database and its backup.
2. On a fresh host or new PostgreSQL database, restore a chosen dump with `pg_restore --exit-on-error`. Never restore over a live writable catalog. Update the application DSN to the restored database while the daemon remains stopped.
3. Run `labrelayd maintenance` through a one-shot application container **before starting HTTP service**. Maintenance is durable catalog state. Run `migrate` if required by the chosen image.
4. Run `labrelayd validate-restore`. It reads and hashes **every published exact object version**. Any missing/mismatched version leaves maintenance enabled; investigate without deleting storage. Only a successful check disables maintenance.
5. Run `labrelayd audit-storage` and retain its report. Unknown objects/multipart uploads are evidence for review, never automatic deletion candidates. An older catalog can legitimately omit newer objects.
6. Start one daemon, check readiness, list a known revision, fetch it from another machine, and verify checksums. Re-enable timers. Record total recovery time and the backup's age. The pilot's fresh-host recovery target is one hour; the local new-database drill does not establish it.

The automated `scripts/restore-drill.sh` creates a **new local Compose database** and preserves its source catalog. It validates every published exact version, then changes only the cloned catalog to inject a missing version, an empty version, a `null` version, a hash mismatch and a size mismatch. Each failed validation must leave maintenance enabled. Repairing the cloned references must allow validation to reopen it. S3 objects are never changed by these injections.

It writes a dump, timing, failure-check logs and unknown-object report to `results/local/`. Revision and file counts are recorded separately. Drill databases are intentionally retained for inspection. `scripts/restore-drill.sh aws` preserves caller AWS credentials and uses real S3 with the same isolated local PostgreSQL catalog; neither mode is a fresh-host restore.

## Diagnosis

| Symptom | Check/action |
|---|---|
| `401` / `403` | Token validity, revocation, project scope, write access; do not retry indefinitely |
| `409` after failure/expiry | Inspect `status`; repair the cause, then explicitly `sync --retry` |
| Integrity failure | Preserve source, snapshot, receipt and attempt IDs; do not publish or substitute a new expected hash |
| S3/PG unavailable | Restore dependency, restart daemon if exclusive PG connection was lost, rerun `sync` |
| Interrupted PATCH | Resume normally; HEAD offset overrides the journal's cached number |
| Verification pending | Check object-store errors, reconciler timestamp, pending files and disk; data remains unpublished |
| Disk >85% | Stop admitting new work, preserve unpublished snapshots, inspect staging/catalog/logs; prune only eligible published local snapshots |
| Unknown object report | Compare catalog backup age and transition events; preserve until reviewed |
| Failed fetch | Inspect private incomplete staging directory; retry into a new destination |

Metrics expose process CPU/RSS, tusd storage operations, verified bytes/files, verification duration, pending verification and reconciler heartbeat. No path/ingestion labels are emitted. The minute monitor sends heartbeat, disk use and a five-minute verification-stall indicator to CloudWatch. Custom alarms treat missing data as failure. Ensure all alerts actually arrive during the cloud acceptance test.

## Restricted real S3 CI

Create a separate versioned acceptance bucket and an OIDC role trusted only by repository `samkwak188/labrelay` and environment `aws-acceptance`. The trust conditions must include audience `sts.amazonaws.com` and subject `repo:samkwak188/labrelay:environment:aws-acceptance`. Restrict the GitHub environment to the protected `main` branch and require a reviewer. Set environment variables `AWS_ACCEPTANCE_ROLE_ARN` and `AWS_ACCEPTANCE_BUCKET`.

The dedicated bucket must have versioning enabled, all four bucket public-access blocks enabled, and SSE-S3 (`AES256`) default encryption. Before any test upload, a read-only preflight checks these settings and requires the bucket owner to match the assumed role's AWS account. It records the caller ARN, account, region, bucket, source commit and workflow run, without credentials. Custom `S3_ENDPOINT`, `AWS_ENDPOINT_URL` and `AWS_ENDPOINT_URL_S3` overrides are rejected, and [configured profile endpoints are ignored](https://docs.aws.amazon.com/sdkref/latest/guide/feature-ss-endpoints.html).

The role needs these permissions:

- On the acceptance bucket ARN: `s3:ListBucket`, `s3:GetBucketVersioning`, `s3:GetBucketPublicAccessBlock`, `s3:GetEncryptionConfiguration`, `s3:ListBucketMultipartUploads`, `s3:ListBucketVersions`.
- Only on its `uploads/*` objects: `s3:GetObject`, `s3:GetObjectVersion`, `s3:PutObject`, `s3:DeleteObject`, `s3:DeleteObjectVersion`, `s3:AbortMultipartUpload`, `s3:ListMultipartUploadParts`.

It needs no bucket-creation/configuration, EC2 or production bucket access. The [S3 permission reference](https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-with-s3-policy-actions.html) maps the preflight API calls to IAM actions.

The dispatch-only workflow runs integration tests with the race detector, the separate process-crash/network-loss suite, and the restore/failure-injection drill. Runs are serialized and use a local isolated Compose PostgreSQL catalog. Untrusted pull requests receive no cloud credentials. To run from a Linux checkout after configuring an AWS profile and starting the isolated PostgreSQL service:

```bash
export AWS_PROFILE=YOUR_ACCEPTANCE_PROFILE
export AWS_REGION=us-east-2
export S3_BUCKET=YOUR_DEDICATED_ACCEPTANCE_BUCKET
export DATABASE_URL='postgres://labrelay:local-development-only@127.0.0.1:15432/labrelay?sslmode=disable'
docker compose up -d --wait postgres
bash scripts/aws-test.sh
```

Do not source `local-env.sh` for AWS runs. The AWS entry point preserves caller credentials. Logs and JSON evidence go under `results/local/aws-acceptance-*`; GitHub uploads these even after failure, excluding the catalog dump. A passing run establishes real S3 behavior and restore validation with a runner-local catalog, not EC2 operation, alert delivery, or fresh-host recovery.

Acceptance objects are retained; use a dedicated disposable test bucket and review its costs/cleanup separately from published pilot datasets.
