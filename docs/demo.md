# Interruption and recovery demonstration

Run from Linux with the pinned local Compose services available and no existing daemon:

```bash
docker compose up -d --wait
bash scripts/fault-test.sh
```

The script builds both applications and starts a real server process with a fresh project. It terminates the server at creation/verification/publication boundaries, sends a real SIGKILL after an acknowledged 8 MiB PATCH, and runs the collector through a proxy that truncates a PATCH and discards accepted responses. The collector restarts from the authoritative server offset; the final downloaded bytes must match the original deterministic input.

Inspect `results/local/faults-*/results.json` and `server.log`. The recorded run in `evidence/faults-20261002T204031Z/` is an executed local demonstration, not a proposed test. It does not demonstrate two physical machines or AWS.

For a short live walkthrough: show the persisted 8 MiB offset, the server's process exit, the same offset after restart, the three remaining bytes accepted, the published receipt, and the final checksum verification. Then show the response-loss case's bytes sent and success. Do not expose bearer tokens on screen.
