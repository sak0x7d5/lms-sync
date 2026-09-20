# Install lms-sync — https://github.com/sak0x7d5/lms-sync
#
#   irm https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.ps1 | iex
#
# To pass an option, PowerShell needs the script as a block:
#
#   & ([scriptblock]::Create((irm https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.ps1))) -Version v1.3.0
#
# Downloads the binary that matches this machine, checks it against the
# release's SHA256SUMS, puts it in a folder of its own under LOCALAPPDATA and
# adds that folder to your PATH. No administrator rights are needed and
# nothing outside your own profile is touched.
#
# The folder of its own is not tidiness: lms-sync keeps config.toml,
# manifest.json and its Google token beside its own executable. Windows has no
# symlinks without administrator rights, so here the folder itself goes on
# PATH rather than a link to it.
#
# Nothing here asks a question. Run as `irm | iex` there is nothing to read an
# answer from, so every choice is a parameter.

param(
	# A release tag, e.g. v1.3.0. The newest release is used when omitted.
	[string] $Version,

	# Where the binary and its settings live.
	[string] $InstallDir,

	# Leave PATH alone.
	[switch] $NoModifyPath,

	# Install without checking the download against SHA256SUMS.
	[switch] $SkipChecksum,

	# Remove what a previous run installed.
	[switch] $Uninstall
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

# Not cosmetic: Invoke-WebRequest in Windows PowerShell 5.1 is an order of
# magnitude slower with the progress bar drawing.
$ProgressPreference = 'SilentlyContinue'

$Repo = 'sak0x7d5/lms-sync'
$BinName = 'lms-sync'

# The targets release.yml builds. Keep this list and that matrix the same.
$Supported = 'linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64'

# Releases before this one were published without a SHA256SUMS asset.
$FirstChecksummed = 'v1.3.0'

# Overridable so the tests can serve a release from a local origin.
$BaseUrl = if ($env:LMS_SYNC_BASE_URL) { $env:LMS_SYNC_BASE_URL }
	else { "https://github.com/$Repo/releases" }

# ---------------------------------------------------------------------------
# Saying things
# ---------------------------------------------------------------------------

function Write-Step {
	param([string] $Label, [string] $Value)
	Write-Host ("  {0,-12} {1}" -f $Label, $Value)
}

# Fail reports the way the tool itself does (see reportErr in main.go): the
# kind in brackets, then the hint indented under a blank line. The kinds are
# lms-sync's own, from errors.go.
#
# It throws rather than calling exit. Under `irm | iex` there is no script to
# exit from — a bare exit closes the student's console over something as small
# as a mistyped option, where a terminating error returns them to the prompt
# and still leaves a non-zero exit code for a non-interactive run.
function Fail {
	param([string] $Kind, [string] $What, [string] $Hint)
	Write-Host ''
	Write-Host "Error [$Kind]: $What" -ForegroundColor Red
	if ($Hint) {
		Write-Host ''
		foreach ($line in ($Hint -split "`r?`n")) {
			Write-Host ("  " + $line).TrimEnd()
		}
	}
	Write-Host ''
	throw $What
}

# ---------------------------------------------------------------------------
# Pure helpers
#
# These take strings and return strings, so they can be tested without a
# Windows registry to write to — the same reason interpretPicker in browse.go
# is a pure function tested without a display.
# ---------------------------------------------------------------------------

function Get-ArchFromName {
	param([string] $Name)
	switch -Regex ($Name) {
		'^(X64|AMD64|x86_64)$' { return 'amd64' }
		'^(Arm64|ARM64|aarch64)$' { return 'arm64' }
		default { return '' }
	}
}

# Get-ExpectedHash pulls one file's line out of a SHA256SUMS body. The release
# lists every asset and only one of them was downloaded, so the whole file is
# never verified as a unit.
function Get-ExpectedHash {
	param([string] $Sums, [string] $Name)
	foreach ($line in ($Sums -split "`r?`n")) {
		if (-not $line.Trim()) { continue }
		$parts = $line.Trim() -split '\s+', 2
		if ($parts.Count -lt 2) { continue }
		$file = $parts[1].Trim()
		# `sha256sum --binary` writes the name with a leading asterisk.
		if ($file.StartsWith('*')) { $file = $file.Substring(1) }
		if ($file -eq $Name) { return $parts[0].Trim().ToLowerInvariant() }
	}
	return ''
}

# Add-DirToPathValue returns the new PATH, or '' when the directory is already
# in it. Entries are compared with any trailing backslash removed and without
# regard to case, because Windows treats those as the same folder and a second
# copy of the entry is what an installer run twice would otherwise leave.
function Add-DirToPathValue {
	param([string] $Current, [string] $Dir)
	$want = $Dir.TrimEnd('\')
	foreach ($entry in ($Current -split ';')) {
		if (-not $entry.Trim()) { continue }
		if ($entry.Trim().TrimEnd('\') -ieq $want) { return '' }
	}
	if (-not $Current) { return $Dir }
	return ($Current.TrimEnd(';') + ';' + $Dir)
}

# Remove-DirFromPathValue returns the new PATH, or $null when there was
# nothing to take out.
#
# $null rather than '' for that case, because '' is also a real answer: it is
# what comes back when the folder was the only entry there was. Reporting both
# as '' meant the one PATH that most needed rewriting was the one left alone.
function Remove-DirFromPathValue {
	param([string] $Current, [string] $Dir)
	$want = $Dir.TrimEnd('\')
	$kept = @()
	$found = $false
	foreach ($entry in ($Current -split ';')) {
		if (-not $entry.Trim()) { continue }
		if ($entry.Trim().TrimEnd('\') -ieq $want) { $found = $true; continue }
		$kept += $entry
	}
	if (-not $found) { return $null }
	return ($kept -join ';')
}

# ---------------------------------------------------------------------------
# The user's PATH
# ---------------------------------------------------------------------------

# Read-UserPath returns the PATH exactly as the registry holds it, unexpanded.
#
# Three things have to be right here and none of them are the obvious call:
#
#   * $env:Path is the machine PATH and the user PATH already joined. Writing
#     that back to the user PATH copies every machine entry into the user's,
#     permanently.
#   * GetValue expands %USERPROFILE% and the like unless told not to, so
#     writing back what it returned freezes those at today's value.
#   * [Environment]::SetEnvironmentVariable writes a plain string, which
#     destroys the REG_EXPAND_SZ type that makes those entries work at all.
#
# setx is not an alternative: it truncates at 1024 characters and has the same
# type problem.
function Read-UserPath {
	$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $false)
	if (-not $key) { return '' }
	try {
		$value = $key.GetValue('Path', '',
			[Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
		if ($null -eq $value) { return '' }
		return [string] $value
	} finally {
		$key.Dispose()
	}
}

function Write-UserPath {
	param([string] $Value)
	# A PATH this long is already broken; making it longer is not a fix.
	if ($Value.Length -gt 8191) {
		Fail 'config' 'Your PATH is too long to add anything to it safely.' @'
Remove some entries from it, or re-run with -NoModifyPath and add the
folder yourself.
'@
	}
	$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
	if (-not $key) {
		Fail 'filesystem' 'Your environment settings could not be opened.' `
			'Re-run with -NoModifyPath and add the folder to PATH yourself.'
	}
	try {
		$kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
		if ($null -ne $key.GetValue('Path')) { $kind = $key.GetValueKind('Path') }
		$key.SetValue('Path', $Value, $kind)
	} finally {
		$key.Dispose()
	}
}

# ---------------------------------------------------------------------------
# Fetching
# ---------------------------------------------------------------------------

function Get-Url {
	param([string] $Url, [string] $OutFile, [switch] $Quiet)
	try {
		# -UseBasicParsing is a no-op on PowerShell 7 and load-bearing on 5.1,
		# where the default path goes through Internet Explorer's engine and
		# fails outright on a machine where IE has never been opened.
		Invoke-WebRequest -Uri $Url -OutFile $OutFile -UseBasicParsing
		return $true
	} catch {
		if (-not $Quiet) { Write-Verbose $_.Exception.Message }
		return $false
	}
}

# Resolve-Tag reads the newest tag from where github.com redirects
# /releases/latest, rather than from the API: the API allows sixty
# unauthenticated calls an hour per address, and a university behind one
# address gets through those quickly.
function Resolve-Tag {
	if ($Version) { return $Version }
	try {
		$resp = Invoke-WebRequest -Uri "$BaseUrl/latest" -UseBasicParsing `
			-MaximumRedirection 0 -ErrorAction SilentlyContinue
		$location = $resp.Headers.Location
		if ($location -is [array]) { $location = $location[0] }
		if ($location -match '/releases/tag/(.+)$') { return $Matches[1] }
	} catch {
		# A 3xx is an exception on 5.1 with -MaximumRedirection 0; the header
		# is still on the response carried by the error.
		try {
			$location = $_.Exception.Response.Headers.Location
			if ($location -and "$location" -match '/releases/tag/(.+)$') { return $Matches[1] }
		} catch { }
	}
	return ''
}

# ---------------------------------------------------------------------------

function Get-InstallDir {
	if ($InstallDir) { return $InstallDir }
	if (-not $env:LOCALAPPDATA) {
		Fail 'config' 'LOCALAPPDATA is not set, so there is nowhere obvious to install to.' `
			'Pass -InstallDir with a folder of your own.'
	}
	return (Join-Path $env:LOCALAPPDATA 'Programs\lms-sync')
}

function Assert-NotRunning {
	param([string] $Exe)
	if (-not (Test-Path $Exe)) { return }
	$running = @(Get-Process -Name $BinName -ErrorAction SilentlyContinue)
	if ($running.Count -gt 0) {
		Fail 'filesystem' 'lms-sync is running, so its program file cannot be replaced.' @'
Close the lms-sync window, and stop any assistant using it over --mcp,
then run this again.
'@
	}
}

function Install-LmsSync {
	Write-Host ''
	Write-Host 'lms-sync installer'
	Write-Host ''

	$arch = Get-ArchFromName (Get-OsArchitecture)
	$note = ''
	if ($arch -eq 'arm64') {
		# There is no windows/arm64 build. Windows 11 on ARM runs an x64
		# program under emulation; Windows 10 on ARM emulates 32-bit x86
		# only and genuinely cannot, which is worth saying rather than
		# leaving someone with a file that will not start.
		$arch = 'amd64'
		$note = 'this is an ARM PC, so the Intel build is installed — it needs Windows 11'
	}
	if ($arch -ne 'amd64') {
		Fail 'config' "lms-sync has no build for Windows on this processor." @"
lms-sync is built for: $Supported
Anything else Go targets builds from source in one command:
https://github.com/$Repo#build-from-source
"@
	}

	$asset = "$BinName-windows-$arch.exe"
	$dir = Get-InstallDir
	$exe = Join-Path $dir "$BinName.exe"

	Assert-NotRunning $exe

	$tag = Resolve-Tag
	Write-Step 'target' ('windows/' + $arch)
	Write-Step 'version' $(if ($tag) { $tag } else { 'latest' })
	if ($note) { Write-Step '' $note }

	$assetBase = if ($tag) { "$BaseUrl/download/$tag" } else { "$BaseUrl/latest/download" }

	New-Item -ItemType Directory -Force -Path $dir | Out-Null
	$tmp = Join-Path $dir (".install-" + $PID)
	New-Item -ItemType Directory -Force -Path $tmp | Out-Null

	try {
		Write-Step 'downloading' $asset
		$dl = Join-Path $tmp $asset
		if (-not (Get-Url "$assetBase/$asset" $dl)) {
			Fail 'network' "Could not download $asset." @"
Either that release has no build for this machine, or the network
refused the request. The releases are listed at
https://github.com/$Repo/releases
"@
		}

		if ($SkipChecksum) {
			Write-Host ''
			Write-Host 'Warning: installing without checking SHA256SUMS, because -SkipChecksum was passed' -ForegroundColor Yellow
		} else {
			$sumsFile = Join-Path $tmp 'SHA256SUMS'
			if (-not (Get-Url "$assetBase/SHA256SUMS" $sumsFile -Quiet)) {
				Fail 'config' 'That release publishes no SHA256SUMS, so the download cannot be checked.' @"
Releases before $FirstChecksummed were published without one.
Install a current release, or re-run with -SkipChecksum to accept the
download unverified.
"@
			}
			$want = Get-ExpectedHash (Get-Content $sumsFile -Raw) $asset
			if (-not $want) {
				Fail 'config' "SHA256SUMS does not list $asset." @"
That release does not appear to include a build for this machine.
Built targets: $Supported
"@
			}
			# Get-FileHash answers in upper case and sha256sum writes lower.
			$got = (Get-FileHash -Path $dl -Algorithm SHA256).Hash.ToLowerInvariant()
			if ($want -ne $got) {
				Fail 'config' "$asset does not match the checksum the release published." @"
Nothing was installed. The download was corrupted in transit, or the
file is not the one that release built.

  expected  $want
  got       $got
"@
			}
			Write-Step 'checksum' 'ok'
		}

		try {
			Move-Item -Path $dl -Destination $exe -Force
		} catch [System.IO.IOException] {
			Fail 'filesystem' 'lms-sync.exe is in use and could not be replaced.' @'
Close the lms-sync window, and stop any assistant using it over --mcp,
then run this again.
'@
		}
		Write-Step 'installed' $exe
	} finally {
		Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
	}

	if ($NoModifyPath) {
		Write-Step 'path' 'not changed, because -NoModifyPath was passed'
	} else {
		$new = Add-DirToPathValue (Read-UserPath) $dir
		if ($new) {
			Write-UserPath $new
			Write-Step 'path' "added $dir"
		} else {
			Write-Step 'path' 'already there'
		}
		# So the rest of this window, and the check below, can find it.
		$env:Path = "$dir;$env:Path"
	}

	Write-Host ''
	& $exe --version
	Write-Host ''
	Write-Host 'Open a new terminal, then run lms-sync to open the interface.'
	Write-Host ''
	# The tool resolves a relative destination against the folder its config
	# is in, and ships with the relative default "Courses", so until a
	# destination is set the coursework lands inside the install folder.
	Write-Host "Settings live in $dir. Set a destination in the interface —"
	Write-Host "until you do, courses land in $dir\Courses."
	Write-Host ''
}

function Uninstall-LmsSync {
	Write-Host ''
	Write-Host 'lms-sync installer — removing'
	Write-Host ''

	$dir = Get-InstallDir
	$exe = Join-Path $dir "$BinName.exe"

	Assert-NotRunning $exe
	if (Test-Path $exe) {
		Remove-Item -Force $exe
		Write-Step 'removed' $exe
	}

	$new = Remove-DirFromPathValue (Read-UserPath) $dir
	if ($null -ne $new) {
		Write-UserPath $new
		Write-Step 'path' "removed $dir"
	}

	if (-not (Test-Path $dir)) {
		Write-Host ''
		Write-Host 'Removed.'
		Write-Host ''
		return
	}

	# Remove-Item without -Recurse, which fails on a folder that still has
	# something in it. That is the guard wanted here: this can physically not
	# delete config.toml, a Google token, or a synced library. What is left is
	# listed instead, and deleting it stays the student's decision.
	$left = @(Get-ChildItem -Force $dir -ErrorAction SilentlyContinue)
	if ($left.Count -eq 0) {
		Remove-Item $dir
		Write-Step 'removed' $dir
		Write-Host ''
		Write-Host 'Removed.'
		Write-Host ''
		return
	}

	Write-Host ''
	Write-Host 'The folder is still there, because it is not empty:'
	Write-Host ''
	Write-Host "  $dir"
	foreach ($item in $left) {
		switch ($item.Name) {
			'config.toml' { Write-Host '    config.toml       your LMS username and password' }
			'manifest.json' { Write-Host '    manifest.json     what has already been downloaded' }
			'drive-token.json' { Write-Host '    drive-token.json  your Google sign-in' }
			'drive-push.json' { Write-Host '    drive-push.json   what has been copied to Drive' }
			default { Write-Host "    $($item.Name)" }
		}
	}
	Write-Host ''
	Write-Host 'Delete it yourself once you are sure:'
	Write-Host ''
	Write-Host "  Remove-Item -Recurse '$dir'"
	Write-Host ''
}

function Get-OsArchitecture {
	try {
		return [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
	} catch {
		# Needs .NET Framework 4.7.1, so Windows before 1709 comes here. The
		# ARCHITEW6432 variable is read first because PROCESSOR_ARCHITECTURE
		# reports x86 to a 32-bit PowerShell on a 64-bit machine.
		if ($env:PROCESSOR_ARCHITEW6432) { return $env:PROCESSOR_ARCHITEW6432 }
		if ($env:PROCESSOR_ARCHITECTURE) { return $env:PROCESSOR_ARCHITECTURE }
		return ''
	}
}

function Invoke-Main {
	if ($PSVersionTable.PSVersion.Major -lt 5) {
		Fail 'config' "This needs PowerShell 5 or newer; this is $($PSVersionTable.PSVersion)." `
			'Windows 10 and 11 ship with 5.1 built in.'
	}

	# $IsWindows only exists on PowerShell 6+; on 5.1 there is no other OS.
	$onWindows = $true
	if (Test-Path variable:IsWindows) { $onWindows = $IsWindows }
	if (-not $onWindows) {
		Fail 'config' 'This is the Windows installer.' @"
On Linux, macOS or Termux, run this instead:

  curl -fsSL https://github.com/$Repo/releases/latest/download/install.sh | sh
"@
	}

	# TLS 1.2 is off by default on Windows PowerShell 5.1, and github.com
	# refuses anything older. Added to what is already enabled rather than
	# replacing it, so TLS 1.3 is not turned off on PowerShell 7.
	try {
		[Net.ServicePointManager]::SecurityProtocol =
			[Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
	} catch { }

	if ($Uninstall) { Uninstall-LmsSync } else { Install-LmsSync }
}

# Only run when this is the script being executed, so that a test can dot-source
# the file for its pure functions without installing anything.
if (-not $env:LMS_SYNC_PS_NO_MAIN) { Invoke-Main }
