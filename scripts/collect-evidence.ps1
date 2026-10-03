$ErrorActionPreference = 'Stop'
$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$dest = Join-Path $root 'evidence'
New-Item -ItemType Directory -Path $dest -Force | Out-Null
$names = @('checks-20261002T203954Z','faults-20261002T204031Z','dependencies-20261002T203344Z','restore-20261002T201822Z','workloads-20261002T202739Z','benchmark-20261002T202116Z','benchmark-20261002T204308Z')
foreach ($name in $names) {
    $source = Join-Path $root ('results\local\' + $name)
    $out = Join-Path $dest $name
    New-Item -ItemType Directory -Path $out -Force | Out-Null
    foreach ($file in Get-ChildItem -LiteralPath $source -File) {
        if ($file.Extension -in @('.json','.txt','.jsonl')) {
            Copy-Item -LiteralPath $file.FullName -Destination (Join-Path $out $file.Name)
        } elseif ($file.Extension -eq '.log') {
            $inputStream = [IO.File]::OpenRead($file.FullName)
            $outputStream = [IO.File]::Create((Join-Path $out ($file.Name + '.gz')))
            $gzip = New-Object IO.Compression.GZipStream($outputStream, [IO.Compression.CompressionMode]::Compress)
            try { $inputStream.CopyTo($gzip) } finally { $gzip.Dispose(); $outputStream.Dispose(); $inputStream.Dispose() }
        }
    }
}
$files = [ordered]@{}
foreach ($part in @('cmd','internal','tools','scripts','api','deploy','infra')) {
    foreach ($file in Get-ChildItem -LiteralPath (Join-Path $root $part) -File -Recurse) {
        if ($file.FullName -notmatch '\\.terraform\\' -and $file.Extension -in @('.go','.sql','.sh','.py','.ps1','.yaml','.tf','.hcl')) {
            $relative = $file.FullName.Substring($root.Length + 1).Replace('\','/')
            $files[$relative] = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
        }
    }
}
foreach ($name in @('go.mod','go.sum','Dockerfile','compose.yaml')) { $files[$name] = (Get-FileHash -LiteralPath (Join-Path $root $name) -Algorithm SHA256).Hash.ToLowerInvariant() }
$report = [ordered]@{git_commit=$null; note='Workspace source fingerprints; no Git commit is fabricated. Final test/rebuild blocked by host disk exhaustion.'; files=$files}
[IO.File]::WriteAllText((Join-Path $dest 'source-fingerprints.json'), ($report | ConvertTo-Json -Depth 5), (New-Object Text.UTF8Encoding($false)))
Write-Output 'Selected evidence preserved.'
