# Evidence scope

These are selected real local execution results. Logs are gzip-compressed where retained. Source data and database dumps are excluded.

`ci-37163282788/` contains raw fault and restore results retrieved from the passing GitHub CI run for `59702323164fb3887309f9059821aaa0b48de9a4`. It records eight passing process/network fault scenarios and a new-database restore of nine revisions/eleven files with five passing failure injections. `run.json` links the original run and states its scope. Storage is versioned SeaweedFS on the runner; mocked AWS preflight tests are not real AWS evidence.

`release-37163327369/` records the rebuilt `0.1.0-rc.2` packages from the same commit. The original checksum file is preserved, and `result.json` records independent downloaded-archive checksum/content/ELF-header checks. The container build passed; no registry publication or cloud run is represented.

The final host disk-exhaustion incident interrupted the corrected benchmark and prevented the final rebuild/regression pass. Earlier 10-pair timing is superseded; corrected timing has only six completed measured pairs. Neither is a final performance claim.

See `docs/validation.md` for precise passed and unfulfilled gates. No AWS or peer-trial results are represented here.
