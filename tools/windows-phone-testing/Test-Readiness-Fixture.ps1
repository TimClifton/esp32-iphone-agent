param(
    [Parameter(Mandatory)]
    [string]$ScratchRoot
)

$ErrorActionPreference = 'Stop'
$ScratchRoot = [System.IO.Path]::GetFullPath($ScratchRoot)
if (-not (Test-Path -LiteralPath $ScratchRoot -PathType Container)) {
    throw "Scratch root does not exist: $ScratchRoot"
}
$source = Join-Path $PSScriptRoot 'Test-Phone-Run.ps1'

function New-PhoneFixture([string]$Name, [string]$Mode) {
    $folder = Join-Path $ScratchRoot ("readiness-" + $Name + '-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Force $folder | Out-Null
    $wrapper = @'
$mode = '__MODE__'
$statePath = Join-Path $PSScriptRoot 'status-count.txt'
if ($args[0] -eq 'device' -and $args[1] -eq 'info') {
    $count = 0
    if (Test-Path $statePath) { $count = [int](Get-Content $statePath -Raw) }
    $count++
    Set-Content -Path $statePath -Value $count -NoNewline
    if ($mode -eq 'transient' -and $count -eq 1) {
        Write-Output 'session lost (reconnect)'
        & $env:ComSpec /d /c 'exit 1' | Out-Null
        return
    }
    if ($mode -eq 'not-paired') {
        Write-Output 'device is not paired'
        & $env:ComSpec /d /c 'exit 1' | Out-Null
        return
    }
    Write-Output '{"status":"ok","data":{"ap":"streaming","fz":0,"age_ms":100,"hidm":"ble","hid":"connected"}}'
    & $env:ComSpec /d /c 'exit 0' | Out-Null
    return
}
if ($args[0] -eq 'screenshot' -and $args[1] -eq '--output') {
    $output = $args[2]
    New-Item -ItemType Directory -Force (Split-Path $output) | Out-Null
    Set-Content -Path $output -Value 'fixture screenshot' -NoNewline
    Add-Content -Path (Join-Path $PSScriptRoot 'screenshot-count.txt') -Value '1'
    & $env:ComSpec /d /c 'exit 0' | Out-Null
    return
}
throw 'Unexpected fixture request.'
'@.Replace('__MODE__', $Mode)
    Set-Content -Encoding utf8 (Join-Path $folder 'Use-RealBoard.ps1') $wrapper

    $testBody = Get-Content -Raw $source
    # Bypass only session startup; keep the readiness loop and screenshot flow intact.
    $testBody = [regex]::Replace($testBody, '(?ms)^\$basePython = .*?^if \(\$LASTEXITCODE -ne 0\) \{ exit \$LASTEXITCODE \}\r?\n', '')
    Set-Content -Encoding utf8 (Join-Path $folder 'Test-Phone-Run.ps1') $testBody
    return $folder
}

$transientFolder = New-PhoneFixture 'transient-retry' 'transient'
& (Join-Path $transientFolder 'Test-Phone-Run.ps1')
$statusCalls = [int](Get-Content (Join-Path $transientFolder 'status-count.txt') -Raw)
$screenshots = Get-Content (Join-Path $transientFolder 'screenshot-count.txt')
if ($statusCalls -ne 2 -or $screenshots.Count -ne 1) {
    throw "Transient retry fixture expected 2 status calls and 1 screenshot; got $statusCalls and $($screenshots.Count)."
}
Write-Output 'PASS: transient startup failure retried once, then status and screenshot succeeded.'

$notPairedFolder = New-PhoneFixture 'not-paired' 'not-paired'
$failedAsExpected = $false
try {
    & (Join-Path $notPairedFolder 'Test-Phone-Run.ps1')
}
catch {
    $failedAsExpected = $true
}
$statusCalls = [int](Get-Content (Join-Path $notPairedFolder 'status-count.txt') -Raw)
$screenshotCountPath = Join-Path $notPairedFolder 'screenshot-count.txt'
if (-not $failedAsExpected -or $statusCalls -ne 1 -or (Test-Path $screenshotCountPath)) {
    throw 'Not-paired fixture must stop after one status call without taking a screenshot.'
}
Write-Output 'PASS: not-paired status failed without retry or screenshot.'
