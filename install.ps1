# Installs git-s3fs by downloading the latest release binary.
#
#   irm https://raw.githubusercontent.com/SeriousBug/gits3fs/main/install.ps1 | iex
#
# Set $env:VERSION to install a specific release instead of the latest, e.g.:
#
#   $env:VERSION = "v0.1.0"; irm .../install.ps1 | iex

$ErrorActionPreference = "Stop"

$repo = "SeriousBug/gits3fs"

$arch = if ([System.Environment]::Is64BitOperatingSystem) {
	if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
} else {
	throw "unsupported architecture: $env:PROCESSOR_ARCHITECTURE"
}

$version = $env:VERSION
if (-not $version) {
	$release = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest"
	$version = $release.tag_name
}
if (-not $version) {
	throw "could not determine the latest release version"
}

$asset = "git-s3fs-windows-$arch.exe"
$url = "https://github.com/$repo/releases/download/$version/$asset"

$installDir = Join-Path $env:LOCALAPPDATA "git-s3fs\bin"
New-Item -ItemType Directory -Force -Path $installDir | Out-Null
$dest = Join-Path $installDir "git-s3fs.exe"

Write-Host "Downloading $asset ($version)..."
Invoke-WebRequest -Uri $url -OutFile $dest

Write-Host "Installed git-s3fs to $dest"

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$pathEntries = $userPath -split ";"
if ($pathEntries -contains $installDir) {
	Write-Host "$installDir is already on your PATH. Run 'git s3fs --help' to get started."
} else {
	[Environment]::SetEnvironmentVariable("Path", "$userPath;$installDir", "User")
	Write-Host ""
	Write-Host "Added $installDir to your PATH."
	Write-Host "Open a new terminal, then run 'git s3fs --help' to get started."
}
