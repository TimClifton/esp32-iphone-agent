# Invoke this inside phone-session.py for real-board commands.
$basePython = & (Join-Path $PSScriptRoot '.venv/Scripts/python.exe') -c 'import sys; print(sys._base_executable)'
if ($LASTEXITCODE -ne 0 -or -not $basePython) { exit 1 }
& $basePython (Join-Path $PSScriptRoot 'phone-session.py') --cli @args
exit $LASTEXITCODE
