# Project-local MCP launcher for the physical phone.
$basePython = & (Join-Path $PSScriptRoot '.venv/Scripts/python.exe') -c 'import sys; print(sys._base_executable)'
if ($LASTEXITCODE -ne 0 -or -not $basePython) { exit 1 }
$runner = Join-Path $PSScriptRoot 'phone-session.py'
& $basePython $runner -- $basePython $runner --cli mcp
exit $LASTEXITCODE
