<#
.SYNOPSIS
  Types whatever is on the clipboard, one key at a time, like a person would.

.DESCRIPTION
  For screen recordings: copy the question, click into the chat box, press
  Ctrl+Alt+V, and it is typed at a steady human pace instead of appearing in
  one paste. Works in any window, any characters (it sends Unicode, so quotes,
  dashes and Urdu all survive).

  Hotkeys while it runs:
    Ctrl+Alt+V   type the clipboard into the focused window
    Esc          stop typing (mid-sentence)
    Ctrl+Alt+Q   quit the script

  Typing also stops by itself if the focused window changes, so a stray click
  never sends the rest of the text somewhere else.

  Line breaks are typed as Shift+Enter, which claude.ai and most chat boxes
  treat as a new line rather than "send". Nothing is sent unless you pass
  -Send.

.PARAMETER Speed
  Average characters per second (default 14, a quick but believable typist).

.PARAMETER Send
  Press Enter once the text is typed.

.PARAMETER Countdown
  Type once after this many seconds instead of waiting for the hotkey.

.PARAMETER Text
  Type this instead of the clipboard.

.PARAMETER Log
  Folder to write one CSV per typing run (keys-YYYYMMDD-HHMMSS.csv), one row
  per key at the moment it was sent: index, char, kind (char/space/newline/
  enter), epoch_ms (wall clock, UTC) and t (seconds since the first key).
  To lay key sounds under a recording, find the video time the first
  character appears and add each row's t to it.

.PARAMETER DryRun
  Print what would be typed and how long it would take; type nothing.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File type-clipboard.ps1
.EXAMPLE
  powershell -ExecutionPolicy Bypass -File type-clipboard.ps1 -Speed 10 -Send
#>
param(
  [double]$Speed = 14,
  [switch]$Send,
  [int]$Countdown = -1,
  [string]$Text,
  [string]$Log,
  [switch]$DryRun,
  [int]$Seed = 0
)
$ErrorActionPreference = 'Stop'

Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;

public static class SmoothType {
  [StructLayout(LayoutKind.Sequential)]
  struct MOUSEINPUT { public int dx, dy; public uint mouseData, dwFlags, time; public IntPtr extra; }
  [StructLayout(LayoutKind.Sequential)]
  struct KEYBDINPUT { public ushort wVk, wScan; public uint dwFlags, time; public IntPtr extra; }
  [StructLayout(LayoutKind.Explicit)]
  struct UNION { [FieldOffset(0)] public MOUSEINPUT mi; [FieldOffset(0)] public KEYBDINPUT ki; }
  [StructLayout(LayoutKind.Sequential)]
  struct INPUT { public uint type; public UNION u; }

  [DllImport("user32.dll", SetLastError = true)] static extern uint SendInput(uint n, INPUT[] inputs, int size);
  [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
  // Sleep's default granularity is ~15 ms, coarse enough to bunch keys.
  [DllImport("winmm.dll")] public static extern uint timeBeginPeriod(uint ms);
  [DllImport("winmm.dll")] public static extern uint timeEndPeriod(uint ms);
  [DllImport("user32.dll")] public static extern short GetAsyncKeyState(int vk);
  [DllImport("user32.dll")] public static extern bool RegisterHotKey(IntPtr hwnd, int id, uint mods, uint vk);
  [DllImport("user32.dll")] public static extern bool UnregisterHotKey(IntPtr hwnd, int id);
  [DllImport("user32.dll")] static extern bool PeekMessage(out MSG msg, IntPtr hwnd, uint min, uint max, uint remove);
  [StructLayout(LayoutKind.Sequential)]
  public struct MSG { public IntPtr hwnd; public uint message; public IntPtr wParam, lParam; public uint time; public int x, y; }

  const uint KEYUP = 0x2, UNICODE = 0x4;

  static void Send(INPUT[] a) {
    if (SendInput((uint)a.Length, a, Marshal.SizeOf(typeof(INPUT))) != a.Length)
      throw new Exception("SendInput was refused (is the target window running as administrator?)");
  }
  static INPUT Key(ushort vk, ushort scan, uint flags) {
    INPUT i = new INPUT(); i.type = 1;
    i.u.ki.wVk = vk; i.u.ki.wScan = scan; i.u.ki.dwFlags = flags;
    return i;
  }
  // One UTF-16 unit, down and up. A surrogate pair is two calls, which is
  // what Windows expects.
  public static void Unit(char c) { Send(new[] { Key(0, c, UNICODE), Key(0, c, UNICODE | KEYUP) }); }
  public static void ShiftEnter() {
    Send(new[] { Key(0x10, 0, 0), Key(0x0D, 0, 0), Key(0x0D, 0, KEYUP), Key(0x10, 0, KEYUP) });
  }
  public static void Enter() { Send(new[] { Key(0x0D, 0, 0), Key(0x0D, 0, KEYUP) }); }

  public static bool Down(int vk) { return (GetAsyncKeyState(vk) & 0x8000) != 0; }
  // Returns the hotkey id that fired, or 0.
  public static int PollHotkey() {
    MSG m;
    while (PeekMessage(out m, IntPtr.Zero, 0x0312, 0x0312, 1)) return (int)m.wParam;
    return 0;
  }
}
'@

$rng = if ($Seed) { [Random]::new($Seed) } else { [Random]::new() }

# Seconds to wait before each character. A steady base with jitter, a little
# longer at spaces, a real pause after punctuation, and an occasional hesitation.
function Get-Delays([string]$s) {
  $base = 1.0 / $Speed
  $out = New-Object 'System.Collections.Generic.List[double]'
  $prev = ''
  foreach ($c in $s.ToCharArray()) {
    $d = $base * (0.55 + $rng.NextDouble() * 0.9)
    if ($c -eq ' ') { $d += $base * 0.6 }
    if ($prev -match '[.,;:!?]') { $d += 0.12 + $rng.NextDouble() * 0.18 }
    if ($prev -eq "`n") { $d += 0.25 }
    if ($rng.NextDouble() -lt 0.03) { $d += 0.2 + $rng.NextDouble() * 0.25 }
    if ([char]::IsLowSurrogate($c)) { $d = 0 }   # second half of one character
    $out.Add($d); $prev = [string]$c
  }
  return ,$out
}

function Get-TextToType {
  $t = if ($PSBoundParameters.ContainsKey('Text') -or $Text) { $Text } else { Get-Clipboard -Raw }
  if ($null -eq $t) { return '' }
  # CRLF and CR become one line break; tabs would move focus, so they become spaces
  return ($t -replace "`r`n", "`n" -replace "`r", "`n" -replace "`t", '    ').TrimEnd("`n")
}

function Wait-ModifiersUp {
  # Ctrl and Alt are still held from the hotkey; typing now would send
  # Ctrl+Alt+letter (AltGr on many layouts) instead of letters.
  while ([SmoothType]::Down(0x11) -or [SmoothType]::Down(0x12) -or [SmoothType]::Down(0x10) -or
         [SmoothType]::Down(0x5B) -or [SmoothType]::Down(0x5C)) { Start-Sleep -Milliseconds 20 }
  Start-Sleep -Milliseconds 250
}

function Invoke-Type([string]$s) {
  if (-not $s) { Write-Host 'Nothing to type: the clipboard is empty or not text.'; return }
  $delays = Get-Delays $s
  $total = ($delays | Measure-Object -Sum).Sum
  if ($DryRun) {
    Write-Host ("{0} characters, about {1:N1} s at {2} chars/s{3}" -f $s.Length, $total, $Speed, $(if ($Send) { ', then Enter' } else { '' }))
    Write-Host '---'; Write-Host $s; Write-Host '---'
    return
  }
  $target = [SmoothType]::GetForegroundWindow()
  # One monotonic clock for the whole run; wall time is derived from it, so
  # the gaps between keys are exact even if the system clock is adjusted.
  $epoch0 = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
  $sw = [Diagnostics.Stopwatch]::StartNew()
  $keys = New-Object 'System.Collections.Generic.List[object]'
  $stamp = {
    param($ch, $kind)
    $ms = $sw.Elapsed.TotalMilliseconds
    $keys.Add([pscustomobject]@{ index = $keys.Count; char = $ch; kind = $kind; ms = $ms })
  }
  $why = $null
  [void][SmoothType]::timeBeginPeriod(1)
  try {
    $last = 0.0
    for ($i = 0; $i -lt $s.Length; $i++) {
      # Each gap is measured from the previous key. Catching up to a fixed
      # schedule instead fires two keys back to back after any late wake-up,
      # which reads (and sounds) like a stutter.
      $wait = [int](($last + $delays[$i] - $sw.Elapsed.TotalSeconds) * 1000)
      if ($wait -gt 0) { [Threading.Thread]::Sleep($wait) }
      $last = $sw.Elapsed.TotalSeconds
      if ([SmoothType]::Down(0x1B)) { $why = "Stopped (Esc) after $i of $($s.Length) characters."; break }
      if ([SmoothType]::GetForegroundWindow() -ne $target) { $why = "Stopped: the focused window changed after $i of $($s.Length) characters."; break }
      $c = $s[$i]
      if ($c -eq "`n") { [SmoothType]::ShiftEnter(); & $stamp '' 'newline' }
      else {
        [SmoothType]::Unit($c)
        # the second half of a surrogate pair is the same key, not another one
        if (-not [char]::IsLowSurrogate($c)) { & $stamp $(if ($c -eq ' ') { '' } else { [string]$c }) $(if ($c -eq ' ') { 'space' } else { 'char' }) }
      }
    }
    if (-not $why -and $Send) { Start-Sleep -Milliseconds 400; [SmoothType]::Enter(); & $stamp '' 'enter' }
  } finally {
    [void][SmoothType]::timeEndPeriod(1)
    if ($Log -and $keys.Count) {
      New-Item -ItemType Directory -Force -Path $Log | Out-Null
      $first = $keys[0].ms
      $file = Join-Path $Log ('keys-{0}.csv' -f [DateTime]::Now.ToString('yyyyMMdd-HHmmss'))
      $keys | ForEach-Object {
        [pscustomobject]@{
          index = $_.index; char = $_.char; kind = $_.kind
          epoch_ms = $epoch0 + [long][Math]::Round($_.ms)
          t = [Math]::Round(($_.ms - $first) / 1000, 3).ToString([Globalization.CultureInfo]::InvariantCulture)
        }
      } | Export-Csv -NoTypeInformation -Encoding UTF8 -Path $file
      Write-Host "Logged $($keys.Count) keys to $file"
    }
  }
  if ($why) { Write-Host $why; return }
  Write-Host ("Typed {0} characters in {1:N1} s{2}." -f $s.Length, $sw.Elapsed.TotalSeconds, $(if ($Send) { ' and pressed Enter' } else { '' }))
}

if ($DryRun) { Invoke-Type (Get-TextToType); return }

if ($Countdown -ge 0) {
  $s = Get-TextToType
  for ($n = $Countdown; $n -gt 0; $n--) { Write-Host "Typing in $n... click into the box"; Start-Sleep 1 }
  Invoke-Type $s
  return
}

# Hotkey mode. MOD_ALT 1, MOD_CONTROL 2, MOD_NOREPEAT 0x4000.
$TYPE = 1; $QUIT = 2
if (-not [SmoothType]::RegisterHotKey([IntPtr]::Zero, $TYPE, 0x4003, 0x56)) { throw 'Ctrl+Alt+V is taken by another program.' }
if (-not [SmoothType]::RegisterHotKey([IntPtr]::Zero, $QUIT, 0x4003, 0x51)) { throw 'Ctrl+Alt+Q is taken by another program.' }
Write-Host "Ready. Copy text, click into a box, press Ctrl+Alt+V. Esc stops typing, Ctrl+Alt+Q quits. ($Speed chars/s$(if ($Send) { ', Enter at the end' })$(if ($Log) { ", logging to $Log" }))"
try {
  while ($true) {
    switch ([SmoothType]::PollHotkey()) {
      $TYPE { Wait-ModifiersUp; Invoke-Type (Get-TextToType) }
      $QUIT { Write-Host 'Bye.'; return }
    }
    Start-Sleep -Milliseconds 30
  }
} finally {
  [void][SmoothType]::UnregisterHotKey([IntPtr]::Zero, $TYPE)
  [void][SmoothType]::UnregisterHotKey([IntPtr]::Zero, $QUIT)
}
