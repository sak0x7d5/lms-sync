# Install lms-sync -- https://github.com/sak0x7d5/lms-sync
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
# It also keeps a verified copy of itself in that folder and registers a
# Task Scheduler task that runs the copy with -Update once a day, so nobody
# has to come back to GitHub for a fix. -NoAutoUpdate turns that off.
#
# Nothing here asks a question. Run as `irm | iex` there is nothing to read an
# answer from, so every choice is a parameter.
#
# This file is ASCII on purpose, and CI holds it to that. The daily task runs
# it with `powershell.exe -File`, and Windows PowerShell 5.1 reads a file with
# no byte-order mark in the ANSI code page: the last byte of a UTF-8 em dash
# is then a curly closing quote, which PowerShell honours as one, and a
# string holding one ends in the middle.

param(
	# A release tag, e.g. v1.3.0. The newest release is used when omitted.
	[string] $Version,

	# Where the binary and its settings live.
	[string] $InstallDir,

	# Leave PATH alone.
	[switch] $NoModifyPath,

	# Do not register the daily update task.
	[switch] $NoAutoUpdate,

	# Install without checking the download against SHA256SUMS.
	[switch] $SkipChecksum,

	# Bring an existing install up to the newest release. This is what the
	# daily task runs.
	[switch] $Update,

	# Remove what a previous run installed, the daily task included.
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
$Supported = @'
linux/amd64, linux/arm64, darwin/amd64, darwin/arm64,
windows/amd64, windows/arm64
'@

# Releases before this one were published without a SHA256SUMS asset.
$FirstChecksummed = 'v1.3.0'

# What the daily update is registered as, so a second install replaces it and
# -Uninstall can find it.
$TaskName = 'lms-sync update'

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
# exit from -- a bare exit closes the student's console over something as small
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
# Windows registry to write to -- the same reason interpretPicker in browse.go
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

# Save-Installer leaves a verified copy of this script beside the binary, which
# is what the daily task runs. It is downloaded from the release rather than
# copied, because under `irm | iex` there is no file to copy -- and a copy that
# went through SHA256SUMS is one an unattended run can trust exactly as far as
# the binary beside it.
#
# It never fails the install: the binary is in place by now, and a release
# that predates the installers being checksummed simply leaves no copy and no
# task. Returns whether a copy was kept.
function Save-Installer {
	param([string] $AssetBase, [string] $Tmp, [string] $Dir)
	$sumsFile = Join-Path $Tmp 'SHA256SUMS'
	if (-not (Test-Path $sumsFile)) {
		if (-not (Get-Url "$AssetBase/SHA256SUMS" $sumsFile -Quiet)) { return $false }
	}
	$want = Get-ExpectedHash (Get-Content $sumsFile -Raw) 'install.ps1'
	if (-not $want) { return $false }
	$dl = Join-Path $Tmp 'install.ps1'
	if (-not (Get-Url "$AssetBase/install.ps1" $dl -Quiet)) { return $false }
	$got = (Get-FileHash -Path $dl -Algorithm SHA256).Hash.ToLowerInvariant()
	if ($want -ne $got) { return $false }
	try {
		Move-Item -Path $dl -Destination (Join-Path $Dir 'install.ps1') -Force
	} catch {
		return $false
	}
	return $true
}

# Set-ExeFile puts a new lms-sync.exe in place even while the old one runs.
#
# Windows will not overwrite a program that is running, but it will rename
# one: the process keeps running from the renamed file, and the new one takes
# the name. That matters here and not at install, because an MCP server lives
# as long as the assistant that started it, and an update that waited for it
# to exit might wait for weeks. The renamed file is deleted as soon as nothing
# holds it, which for a running one is the next update.
function Set-ExeFile {
	param([string] $New, [string] $Exe)
	$old = "$Exe.old"
	if (Test-Path $old) { Remove-Item -Force $old -ErrorAction SilentlyContinue }
	if (Test-Path $old) { $old = "$Exe.old-$PID" }
	if (Test-Path $Exe) { Move-Item -Path $Exe -Destination $old -Force }
	try {
		Move-Item -Path $New -Destination $Exe -Force
	} catch {
		if (Test-Path $old) { Move-Item -Path $old -Destination $Exe -Force }
		throw
	}
	Remove-Item -Force $old -ErrorAction SilentlyContinue
}

function Remove-OldExe {
	param([string] $Dir)
	Get-ChildItem -Path $Dir -Filter "$BinName.exe.old*" -Force -ErrorAction SilentlyContinue |
		Remove-Item -Force -ErrorAction SilentlyContinue
}

# Get-TaskArgument quotes a path for powershell.exe's command line, where a
# backslash before a closing quote escapes it: "C:\dir\" is read as C:\dir"
# and the rest of the line with it.
function Get-TaskArgument {
	param([string] $Path)
	$p = $Path.TrimEnd('\')
	if ($p.EndsWith(':')) { $p += '\.' }
	return '"' + $p + '"'
}

# Register-UpdateTask runs the kept copy with -Update once a day, at a random
# time in the day: random so a campus of installs behind one address does not
# reach github.com in the same minute, in the day because a laptop is shut at
# night. StartWhenAvailable makes up a day the machine was off.
#
# It runs as the student, only while they are signed in, and needs no
# administrator rights. A task that cannot be registered is not an install
# failure: the tool works as before, and the summary says how to update.
function Register-UpdateTask {
	param([string] $Dir)
	$script = Join-Path $Dir 'install.ps1'
	if (-not (Get-Command Register-ScheduledTask -ErrorAction SilentlyContinue)) {
		Write-Step 'updates' 'not scheduled: this PowerShell has no Task Scheduler cmdlets'
		Write-Step '' "to update by hand: & '$script' -Update"
		return
	}
	$ps = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
	# -ExecutionPolicy Bypass because the default policy on a Windows client
	# refuses to run any script file at all; it applies to this one process.
	$arguments = '-NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden ' +
		'-File ' + (Get-TaskArgument $script) + ' -Update -InstallDir ' + (Get-TaskArgument $Dir)
	$at = (Get-Date).Date.AddHours(10 + (Get-Random -Maximum 8)).AddMinutes((Get-Random -Maximum 60))
	try {
		$action = New-ScheduledTaskAction -Execute $ps -Argument $arguments -WorkingDirectory $Dir
		$trigger = New-ScheduledTaskTrigger -Daily -At $at
		$settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -AllowStartIfOnBatteries `
			-DontStopIfGoingOnBatteries -MultipleInstances IgnoreNew `
			-ExecutionTimeLimit (New-TimeSpan -Minutes 30)
		Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
			-Settings $settings -Force -ErrorAction Stop `
			-Description 'Updates lms-sync to the newest release. Remove with the installer''s -Uninstall.' |
			Out-Null
		Write-Step 'updates' "checked daily (Task Scheduler: $TaskName)"
	} catch {
		Write-Step 'updates' ('not scheduled: ' + $_.Exception.Message.Trim())
		Write-Step '' "to update by hand: & '$script' -Update"
	}
}

function Unregister-UpdateTask {
	param([switch] $Quiet)
	if (-not (Get-Command Get-ScheduledTask -ErrorAction SilentlyContinue)) { return }
	$task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
	if (-not $task) { return }
	try {
		Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction Stop
		if (-not $Quiet) { Write-Step 'removed' "the daily update ($TaskName)" }
	} catch {
		Write-Step 'kept' ("the task '$TaskName' could not be removed: " + $_.Exception.Message.Trim())
	}
}

function Set-UpdateSchedule {
	param([string] $Dir, [bool] $Kept)
	if ($NoAutoUpdate -or $env:LMS_SYNC_NO_AUTO_UPDATE) {
		Unregister-UpdateTask -Quiet
		Write-Step 'updates' 'not scheduled, because -NoAutoUpdate was passed'
		return
	}
	# A pinned release is a request for that release. Updating it tomorrow
	# would quietly undo the one choice made on purpose, so an earlier
	# install's task is taken out rather than left to do exactly that.
	if ($Version) {
		Unregister-UpdateTask -Quiet
		Write-Step 'updates' "not scheduled, because -Version pinned $Version"
		return
	}
	if (-not $Kept) {
		Write-Step 'updates' 'not scheduled: that release has no verifiable install.ps1'
		return
	}
	Register-UpdateTask $Dir
}

function Install-LmsSync {
	Write-Host ''
	Write-Host 'lms-sync installer'
	Write-Host ''

	# An ARM PC gets the ARM build. Emulating the Intel one would work on
	# Windows 11 and not on Windows 10, whose ARM emulation is 32-bit x86
	# only -- so the native build is the one that always starts.
	$arch = Get-ArchFromName (Get-OsArchitecture)
	if ($arch -ne 'amd64' -and $arch -ne 'arm64') {
		Fail 'config' 'lms-sync has no build for Windows on this processor.' @"
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

	$assetBase = if ($tag) { "$BaseUrl/download/$tag" } else { "$BaseUrl/latest/download" }

	New-Item -ItemType Directory -Force -Path $dir | Out-Null
	$tmp = Join-Path $dir (".install-" + $PID)
	New-Item -ItemType Directory -Force -Path $tmp | Out-Null
	Remove-OldExe $dir
	$kept = $false

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
		$kept = Save-Installer $assetBase $tmp $dir
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

	Set-UpdateSchedule $dir $kept

	Write-Host ''
	& $exe --version
	Write-Host ''
	Write-Host 'Open a new terminal, then run lms-sync to open the interface.'
	Write-Host ''
	# The tool resolves a relative destination against the folder its config
	# is in, and ships with the relative default "Courses", so until a
	# destination is set the coursework lands inside the install folder.
	Write-Host "Settings live in $dir. Set a destination in the interface;"
	Write-Host "until you do, courses land in $dir\Courses."
	Write-Host ''
}

# Update-LmsSync brings an existing install up to the newest release, and
# changes nothing when it is already there.
#
# "Already there" is decided by hash rather than version string: the installed
# exe is a release asset byte for byte, so comparing it with its SHA256SUMS
# line needs no version parsing and costs one small download a day.
#
# It never touches PATH or the task. Those were settled at install.
function Update-LmsSync {
	$dir = Get-InstallDir
	$exe = Join-Path $dir "$BinName.exe"
	if (-not (Test-Path $exe)) {
		Fail 'filesystem' "There is no lms-sync in $dir to update." `
			'Install it first, or pass the folder it is in with -InstallDir.'
	}

	# Run by the task, nobody sees the output, so it goes to a file holding
	# the last run, where "why has it not updated?" can be answered.
	$transcript = $false
	try {
		Start-Transcript -Path (Join-Path $dir 'update.log') -Force | Out-Null
		$transcript = $true
	} catch { }

	try {
		Write-Host ''
		Write-Host ('lms-sync update, ' + (Get-Date -Format 'yyyy-MM-dd HH:mm:ss zzz'))
		Write-Host ''

		Remove-OldExe $dir
		$arch = Get-ArchFromName (Get-OsArchitecture)
		$asset = "$BinName-windows-$arch.exe"

		# The tag pins the checksum file and the exe to one release, so a
		# release published between the two downloads cannot pair them wrongly.
		$tag = Resolve-Tag
		Write-Step 'target' ('windows/' + $arch)
		Write-Step 'newest' $(if ($tag) { $tag } else { 'latest' })
		$assetBase = if ($tag) { "$BaseUrl/download/$tag" } else { "$BaseUrl/latest/download" }

		$tmp = Join-Path $dir (".update-" + $PID)
		New-Item -ItemType Directory -Force -Path $tmp | Out-Null
		try {
			$sumsFile = Join-Path $tmp 'SHA256SUMS'
			if (-not (Get-Url "$assetBase/SHA256SUMS" $sumsFile -Quiet)) {
				Fail 'network' 'Could not download SHA256SUMS.' `
					'Nothing was changed. The update is tried again tomorrow.'
			}
			$sums = Get-Content $sumsFile -Raw
			$want = Get-ExpectedHash $sums $asset
			if (-not $want) {
				Fail 'config' "The newest release lists no build for windows/$arch." `
					'Nothing was changed. The update is tried again tomorrow.'
			}
			$have = (Get-FileHash -Path $exe -Algorithm SHA256).Hash.ToLowerInvariant()

			if ($want -eq $have) {
				Write-Step 'binary' 'up to date'
			} else {
				Write-Step 'downloading' $asset
				$dl = Join-Path $tmp $asset
				if (-not (Get-Url "$assetBase/$asset" $dl)) {
					Fail 'network' "Could not download $asset." `
						'Nothing was changed. The update is tried again tomorrow.'
				}
				$got = (Get-FileHash -Path $dl -Algorithm SHA256).Hash.ToLowerInvariant()
				if ($want -ne $got) {
					Fail 'config' "$asset does not match the checksum the release published." @"
Nothing was changed.

  expected  $want
  got       $got
"@
				}
				Write-Step 'checksum' 'ok'
				Set-ExeFile $dl $exe
				Write-Step 'updated' $exe
				try { & $exe --version } catch { Write-Step 'kept' 'the new exe did not report its version' }
			}

			$script = Join-Path $dir 'install.ps1'
			$iwant = Get-ExpectedHash $sums 'install.ps1'
			$ihave = ''
			if (Test-Path $script) {
				$ihave = (Get-FileHash -Path $script -Algorithm SHA256).Hash.ToLowerInvariant()
			}
			if ($iwant -and $iwant -ne $ihave) {
				if (Save-Installer $assetBase $tmp $dir) {
					Write-Step 'updated' $script
				} else {
					Write-Step 'kept' 'the installer copy could not be refreshed; the exe is unaffected'
				}
			}
			Write-Host ''
		} finally {
			Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
		}
	} finally {
		if ($transcript) { Stop-Transcript | Out-Null }
	}
}

function Uninstall-LmsSync {
	Write-Host ''
	Write-Host 'lms-sync installer: removing'
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

	Unregister-UpdateTask

	# The copy the task ran, its log, and an exe renamed aside by an update
	# are the installer's own files, not the student's, so they go too;
	# otherwise they alone would keep the folder of an unused install alive.
	if (Test-Path $dir) {
		Remove-OldExe $dir
		foreach ($own in @('install.ps1', 'update.log')) {
			$path = Join-Path $dir $own
			if (Test-Path $path) {
				Remove-Item -Force $path
				Write-Step 'removed' $path
			}
		}
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

	# An unattended run replacing a program is the one place where skipping
	# the check can never be what was meant.
	if ($Update -and $SkipChecksum) {
		Fail 'config' '-Update always verifies what it installs.' 'Drop -SkipChecksum.'
	}

	if ($Uninstall) { Uninstall-LmsSync }
	elseif ($Update) { Update-LmsSync }
	else { Install-LmsSync }
}

# Only run when this is the script being executed, so that a test can dot-source
# the file for its pure functions without installing anything.
if (-not $env:LMS_SYNC_PS_NO_MAIN) { Invoke-Main }
