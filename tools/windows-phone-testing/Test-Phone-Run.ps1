param([switch]$RunHomeCheck)
$ErrorActionPreference = 'Stop'
$basePython = & (Join-Path $PSScriptRoot '.venv/Scripts/python.exe') -c 'import sys; print(sys._base_executable)'
if ($LASTEXITCODE -ne 0 -or -not $basePython) { exit 1 }
& $basePython (Join-Path $PSScriptRoot 'phone-session.py') --check
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
$runFolder = Join-Path $PSScriptRoot ('phone-tests/' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
New-Item -ItemType Directory -Force $runFolder | Out-Null
$statusDeadline = (Get-Date).AddSeconds(10)
do {
    $infoRaw = & (Join-Path $PSScriptRoot 'Use-RealBoard.ps1') device info
    if ($LASTEXITCODE -ne 0) { throw 'Board status failed. No input was sent.' }
    $info = $infoRaw | ConvertFrom-Json
    if ($null -ne $info.data.age_ms) { break }
    Start-Sleep -Milliseconds 300
} while ((Get-Date) -lt $statusDeadline)
if ($info.status -ne 'ok' -or $info.data.ap -ne 'streaming' -or $info.data.fz -eq 1 -or
    $null -eq $info.data.age_ms -or $info.data.age_ms -lt 0 -or $info.data.age_ms -gt 2000 -or
    $info.data.hidm -ne 'ble' -or $info.data.hid -notin @('on','ok','connected')) {
    throw 'Phone not ready: verify Screen Mirroring is active and the ESP32 pointer is connected in AssistiveTouch. No input was sent.'
}
& (Join-Path $PSScriptRoot 'Use-RealBoard.ps1') screenshot --output (Join-Path $runFolder 'before.png')
if ($LASTEXITCODE -ne 0) { throw 'Before screenshot failed. No input was sent.' }
if ($RunHomeCheck) {
    Write-Host 'Sending Home now. Please avoid touching the phone until the after image is captured.'
    & (Join-Path $PSScriptRoot 'Use-RealBoard.ps1') io button HOME
    if ($LASTEXITCODE -ne 0) { throw 'Home command did not succeed.' }
    Start-Sleep -Seconds 2
    & (Join-Path $PSScriptRoot 'Use-RealBoard.ps1') screenshot --output (Join-Path $runFolder 'after.png')
    if ($LASTEXITCODE -ne 0) { throw 'After screenshot failed; navigation result is unverified.' }
}
Write-Host "Evidence saved to $runFolder. Inspect images before calling the test passed."
