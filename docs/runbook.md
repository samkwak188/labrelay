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

The automated `scripts/restore-drill.sh` creates a **new local database** and preserves its source catalog. It writes a dump, timing and unknown-object report to `results/local/`. Drill databases are intentionally retained for inspection.

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

The role needs list/version/multipart operations for that bucket and object access only under `uploads/*`; it needs no EC2 or production bucket access. The dispatch-only workflow tests on real AWS S3 using a local isolated PostgreSQL catalog. Untrusted pull requests run only local compatibility tests and receive no cloud credentials. Acceptance objects are retained; use a dedicated disposable test bucket and review its costs/cleanup separately from published pilot datasets.
