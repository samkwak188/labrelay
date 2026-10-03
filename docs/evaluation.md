# Evaluation and existing-tool comparison

Plain tusd already provides resumable uploads and authoritative server offsets. A manifest/checksum tool can establish that downloaded bytes match producer-declared hashes. That combination can be sufficient when operators manually manage completed directories, durable retry state and publication conventions.

LabRelay adds a dataset-level contract: an immutable snapshot and journal, explicit expected file set, independent server hashing of exact object versions, atomic catalog publication, scoped access, stable receipts and verified downloads. It does not invent resumability or verify scientific validity.

The baseline fixture embeds the same pinned tusd S3 backend with the same 8 MiB preferred parts, buffered-part setting and S3 concurrency. Both clients use `tusgo` and 8 MiB PATCH requests. Runs alternate order against the same local storage backend.

## Raw measurements

`scripts/benchmark.sh --repetitions 10 --bytes 134217728` measures a deterministic SHAKE-generated 128 MiB file after a warm-up. Raw JSON includes preparation, CPU, sampled collector RSS, transfer window, total sync, verification time, spool bytes, bytes sent and paired throughput ratios. Prometheus snapshots preserve server and tusd metrics. A different dataset name forces fresh publication each repetition.

The PATCH window excludes preparation and pre-upload rehashing. End-to-end time includes preparation plus sync. The post-upload interval includes client polling and is not exact database commit latency. Plain tusd does no dataset verification, so compare transfer windows separately from end-to-end time.

Two runs are preserved:

- `benchmark-20261002T202116Z`: warm-up plus ten pairs, **superseded for timing** because LabRelay used wall-clock timestamps. A clock adjustment was observed in a later workload run.
- `benchmark-20261002T204308Z`: corrected monotonic timing; warm-up plus six completed measured pairs before host disk exhaustion interrupted the run. **Incomplete; not a final ten-run benchmark.**

Do not advertise a throughput target as achieved from these runs. The complete CATS bundle and 5,000-file dataset passed end-to-end locally; their timings are single-run observations, not final performance results. The CATS metadata and frames are pinned to upstream commit `26c14d651e2acf06dec4c90a0ba5aae530a3508c`, with the actual source video independently hash-checked.

## Remaining measurement work

Repeat corrected runs after recovering the host. Use ten repetitions after warm-up for all three workloads under recorded network conditions. Capture full server peak RSS, temporary staging disk, complete object-store request counts, retransmission and recovery delay. Preserve failed runs and source/binary identifiers. Profile an observed bottleneck and demonstrate one justified improvement with before/after results.

Same-host loopback tests share caches and host scheduling; sampled RSS can miss short peaks. Tusd counters omit some verifier operations. These limitations prevent extrapolating the local observations to AWS performance or cost.

Targets remain: at least 70% of plain-tusd large-file throughput; collector RSS below 256 MiB and server RSS below 512 MiB; recovery beginning within 120 seconds; no whole-file restart after one interrupted request. The existing evidence supports only the specific local scenarios it records.
