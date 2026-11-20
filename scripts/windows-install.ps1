# Requires -Version 5.1
# Flockadoo Client Windows installation script.
# Mirrors scripts/install.sh: flockadoo on PATH, config at ~/.config/flockadoo.
#
# Usage:
#   $env:PROVIDER_API_KEY="your-key"; irm https://raw.githubusercontent.com/flockadoo/flockadoo/main/scripts/windows-install.ps1 | iex
#
# Optional env: PROVIDER_API_KEY, PROVIDER_TYPE, FLOCKADOO_SKIP_SETUP=1
# With FLOCKADOO_SKIP_SETUP=1 (no wizard): PROVIDER_API_KEY and PROVIDER_MODEL
# are required; LLM_URL and SERVER_PORT are optional.

$ErrorActionPreference = "Stop"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

function Write-Info([string]$Message) { Write-Host $Message -ForegroundColor Cyan }
function Write-Ok([string]$Message) { Write-Host $Message -ForegroundColor Green }
function Write-Warn([string]$Message) { Write-Host $Message -ForegroundColor Yellow }
function Write-Err([string]$Message) { Write-Host $Message -ForegroundColor Red }

function Add-UserPath([string]$Dir) {
    $userPath = [Environment]::GetEnvironmentVariable("PATH", "User")
    if ([string]::IsNullOrEmpty($userPath)) {
        $userPath = ""
    }
    $parts = $userPath -split ";" | Where-Object { $_ -ne "" }
    if ($parts -contains $Dir) {
        return
    }
    $newPath = if ($userPath.Trim() -eq "") { $Dir } else { "$userPath;$Dir" }
    [Environment]::SetEnvironmentVariable("PATH", $newPath, "User")
    if ($env:PATH -notlike "*$Dir*") {
        $env:PATH = "$env:PATH;$Dir"
    }
}

if (-not [Environment]::Is64BitOperatingSystem) {
    Write-Err "32-bit Windows is not supported. Use a 64-bit OS."
    exit 1
}

$SkipSetup = $env:FLOCKADOO_SKIP_SETUP -eq "1"

$ProviderApiKey = $env:PROVIDER_API_KEY
$ProviderType = $env:PROVIDER_TYPE
if ([string]::IsNullOrWhiteSpace($ProviderType)) {
    $ProviderType = "llama_cpp"
}
# Windows has no native vLLM — dashboard copies with PROVIDER_TYPE=vllm install llama.cpp.
$Engine = $ProviderType
if ($Engine -eq "vllm") {
    $Engine = "llama_cpp"
}

$ServerPort = $env:SERVER_PORT
if ([string]::IsNullOrWhiteSpace($ServerPort)) {
    $ServerPort = "8080"
}

$LlmUrl = $env:LLM_URL
if ([string]::IsNullOrWhiteSpace($LlmUrl)) {
    $LlmUrl = "http://127.0.0.1:8081"
}

$HomeDir = $env:USERPROFILE
$BinDir = Join-Path $env:LOCALAPPDATA "flockadoo\bin"
$ConfigDir = Join-Path $HomeDir ".config\flockadoo"
$LogDir = Join-Path $HomeDir ".local\state\flockadoo\log"
$GitHubRepo = "flockadoo/flockadoo"
$ClientZipUrl = "https://github.com/$GitHubRepo/releases/latest/download/flockadoo-windows-amd64.zip"

Write-Info "=== Flockadoo Client Installation ==="
Write-Info "OS: Windows amd64"
Write-Info "Binaries: $BinDir"
Write-Info "Config:   $ConfigDir\config.yaml"

New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
New-Item -ItemType Directory -Path $ConfigDir -Force | Out-Null
New-Item -ItemType Directory -Path $LogDir -Force | Out-Null

$ClientZip = Join-Path $env:TEMP "flockadoo-windows-amd64.zip"
Write-Info "Downloading flockadoo..."
try {
    Invoke-WebRequest -Uri $ClientZipUrl -OutFile $ClientZip -UseBasicParsing
} catch {
    Write-Err "Failed to download flockadoo: $_"
    Write-Warn "Check releases at https://github.com/$GitHubRepo/releases"
    exit 1
}

$ChecksumUrl = "https://github.com/$GitHubRepo/releases/latest/download/checksums.txt"
try {
    $sums = Invoke-WebRequest -Uri $ChecksumUrl -UseBasicParsing
    $expected = $null
    foreach ($line in ($sums.Content -split "`n")) {
        $parts = $line.Trim() -split '\s+'
        if ($parts.Length -ge 2 -and $parts[1] -eq "flockadoo-windows-amd64.zip") {
            $expected = $parts[0].ToLower()
        }
    }
    if (-not $expected) {
        Write-Err "checksums.txt has no entry for flockadoo-windows-amd64.zip"
        exit 1
    }
    $actual = (Get-FileHash -Algorithm SHA256 $ClientZip).Hash.ToLower()
    if ($actual -ne $expected) {
        Write-Err "Checksum mismatch for flockadoo-windows-amd64.zip"
        exit 1
    }
    Write-Ok "Checksum verified."
} catch {
    if ($_.Exception.Message -match "Checksum|checksums.txt has no entry") { throw }
    Write-Warn "No checksums.txt on this release; continuing without verification."
}

$ExtractDir = Join-Path $env:TEMP "flockadoo-extract"
if (Test-Path $ExtractDir) {
    Remove-Item $ExtractDir -Recurse -Force
}
New-Item -ItemType Directory -Path $ExtractDir -Force | Out-Null
Expand-Archive -Path $ClientZip -DestinationPath $ExtractDir -Force

$DownloadedExe = Get-ChildItem -Path $ExtractDir -Filter "flockadoo*.exe" -Recurse | Select-Object -First 1
if (-not $DownloadedExe) {
    Write-Err "Zip did not contain flockadoo.exe"
    exit 1
}
Copy-Item $DownloadedExe.FullName (Join-Path $BinDir "flockadoo.exe") -Force
Remove-Item $ExtractDir -Recurse -Force -ErrorAction SilentlyContinue
Remove-Item $ClientZip -Force -ErrorAction SilentlyContinue
Write-Ok "flockadoo installed."

Add-UserPath $BinDir
Write-Ok "Added $BinDir to your user PATH."

$exe = Join-Path $BinDir "flockadoo.exe"
$ConfigPath = Join-Path $ConfigDir "config.yaml"

if ($SkipSetup) {
    if ([string]::IsNullOrWhiteSpace($ProviderApiKey)) {
        Write-Err "PROVIDER_API_KEY is required when FLOCKADOO_SKIP_SETUP=1"
        exit 1
    }
    # Setup checks the model against the approved catalog and writes the config.
    $setupArgs = @("setup", "--yes", "--no-start", "--config", $ConfigPath,
        "--api-key", $ProviderApiKey, "--engine", $Engine,
        "--llm-url", $LlmUrl, "--port", $ServerPort)
    $ProviderModel = $env:PROVIDER_MODEL
    if (-not [string]::IsNullOrWhiteSpace($ProviderModel)) {
        $setupArgs += @("--model", $ProviderModel)
    }
    & $exe @setupArgs
    if ($LASTEXITCODE -ne 0) {
        Write-Err "Setup failed. Set PROVIDER_MODEL to an approved model that your engine serves."
        Write-Err "List the models this machine can serve: flockadoo compatibility --provider-type llama_cpp"
        exit 1
    }
    Write-Ok "Configuration written."
} else {
    $setupArgs = @("setup", "--config", $ConfigPath)
    if (-not [string]::IsNullOrWhiteSpace($ProviderApiKey)) {
        $setupArgs += @("--api-key", $ProviderApiKey)
    }
    if (-not [string]::IsNullOrWhiteSpace($env:PROVIDER_TYPE)) {
        $setupArgs += @("--engine", $Engine)
    }
    Write-Info "Starting setup wizard..."
    & $exe @setupArgs
    if ($LASTEXITCODE -ne 0) {
        Write-Warn "Setup wizard exited with code $LASTEXITCODE. Re-run: flockadoo setup"
    }
}

$Programs = [Environment]::GetFolderPath("Programs")
$ShortcutDir = Join-Path $Programs "Flockadoo"
New-Item -ItemType Directory -Path $ShortcutDir -Force | Out-Null
$Wsh = New-Object -ComObject WScript.Shell
$Shortcut = $Wsh.CreateShortcut((Join-Path $ShortcutDir "Flockadoo Client.lnk"))
$Shortcut.TargetPath = $exe
$Shortcut.Arguments = "--config `"$ConfigPath`""
$Shortcut.WorkingDirectory = $BinDir
$Shortcut.Description = "Start Flockadoo Provider Client"
$Shortcut.IconLocation = "$exe,0"
$Shortcut.Save()
Write-Ok "Start Menu shortcut created."

Write-Host ""
Write-Ok "Installation complete."
Write-Info "Config: $ConfigPath"
Write-Info "Logs:   $LogDir"
Write-Host ""
Write-Info "Start the client:"
Write-Host "  1. Start Menu -> Flockadoo -> Flockadoo Client  (notification area)"
Write-Host "  OR"
Write-Host "  2. flockadoo"
Write-Host "     (open a new terminal so PATH updates apply; the prompt returns after the dashboard is up, and the client stays in the tray)"
Write-Host ""
Write-Info "Re-run the wizard anytime:  flockadoo setup"
Write-Info "Right-click the Flockadoo tray icon -> Open dashboard for live status."
Write-Host "  flockadoo --console   (terminal UI instead of tray)"
Write-Host ""
Write-Warn "Windows: llama.cpp (CUDA) only. Allow Windows Firewall if prompted. NVIDIA GPU metrics need nvidia-smi on PATH."
Write-Warn "Unsigned download: SmartScreen may show 'Windows protected your PC' -> More info -> Run anyway."
