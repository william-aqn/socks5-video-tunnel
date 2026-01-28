# Playground script for Video-Go
# This script builds the project and runs both server and client in a pre-configured way
# so they can talk to each other through the Debug UI windows.

$ErrorActionPreference = "Continue"

# Get the directory where this script is located
$ScriptDir = $PSScriptRoot
if (-not $ScriptDir) { $ScriptDir = Get-Location }
$ProjectRoot = Split-Path -Parent $ScriptDir
$ExePath = Join-Path $ScriptDir "video-go.exe"

Write-Host "--- Stopping previous instances ---"
Stop-Process -Name "video-go" -ErrorAction SilentlyContinue

Write-Host "--- Cleaning up old logs ---"
$LogFiles = @("server_vgo.log", "client_vgo.log", "debug_server_capture.png")
foreach ($file in $LogFiles) {
    $path = Join-Path $ScriptDir $file
    if (Test-Path $path) {
        Remove-Item $path
        Write-Host "Removed $file"
    }
}

Write-Host "--- Building Video-Go ---"
# Build from project root and output to playground directory
Push-Location $ProjectRoot
go build -o $ExePath .
Pop-Location

# Configuration
$ServerMJPEGPort = 9000
$ClientMJPEGPort = 9001
$SOCKS5Port = 1080

# Window Positions
$ServerDebugX = 0
$ServerDebugY = 0
$ClientDebugX = 700
$ClientDebugY = 0

# Offsets for capturing video from the Debug UI window
# Window frames are ~8px, title bar ~31px, and the video itself is offset by 25px in the client area.
$OffsetX = 8
$OffsetY = 31 + 25 # 56

# Server will capture Client's video automatically by title
Start-Process $ExePath -ArgumentList "-mode=server", "-vcam-port=$ServerMJPEGPort", "-debug-ui", "-debug-x=$ServerDebugX", "-debug-y=$ServerDebugY", "-debug-url=http://127.0.0.1:$ClientMJPEGPort" -WorkingDirectory $ScriptDir

Write-Host "--- Starting Client ---"
# Client will capture Server's video automatically by title
Start-Process $ExePath -ArgumentList "-mode=client", "-local=:$SOCKS5Port", "-vcam-port=$ClientMJPEGPort", "-debug-ui", "-debug-x=$ClientDebugX", "-debug-y=$ClientDebugY", "-debug-url=http://127.0.0.1:$ServerMJPEGPort" -WorkingDirectory $ScriptDir

Write-Host ""
Write-Host "Playground is running!"
Write-Host "SOCKS5 Proxy: localhost:$SOCKS5Port"
Write-Host "Server Debug UI at: ($ServerDebugX, $ServerDebugY)"
Write-Host "Client Debug UI at: ($ClientDebugX, $ClientDebugY)"
Write-Host ""
Write-Host "You can now test it with: curl --socks5 localhost:$SOCKS5Port http://google.com"
Write-Host "Wait a few seconds for windows to initialize before testing."
