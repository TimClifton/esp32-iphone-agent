param([switch]$RunHomeCheck)
$basePython = & (Join-Path $PSScriptRoot '.venv/Scripts/python.exe') -c 'import sys; print(sys._base_executable)'
if ($LASTEXITCODE -ne 0 -or -not $basePython) { exit 1 }
$testArgs = @('pwsh','-NoProfile','-File',(Join-Path $PSScriptRoot 'Test-Phone-Run.ps1'))
if ($RunHomeCheck) { $testArgs += '-RunHomeCheck' }
& $basePython (Join-Path $PSScriptRoot 'phone-session.py') -- @testArgs
exit $LASTEXITCODE
