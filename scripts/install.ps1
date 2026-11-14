# install.ps1 installs Meru on Windows from a GitHub release. Run it in
# PowerShell with:
#
#   irm https://github.com/aarora79/meru/releases/latest/download/install.ps1 | iex
#
# It downloads the release, checks every file against its SHA256SUMS file,
# and puts meru.exe and merud.exe in %LOCALAPPDATA%\Programs\Meru, on your
# PATH. Then it offers the next steps: the desktop app; NVIDIA's driver on
# Windows Server, when an NVIDIA card has none; Ollama; `meru setup`,
# which suggests the answer model for this machine's GPU or memory and
# downloads the models; and merud, started now and at each sign-in by a
# Task Scheduler task. It first lists what it will install and asks once:
# everything, each step in turn, or quit.
#
# Settings, as environment variables, since `irm | iex` can't pass
# parameters:
#   MERU_YES=1          answer "everything" without asking
#   MERU_VERSION=v0.8.0 install that release instead of the latest
#   MERU_FROM=C:\dist   install from a folder holding a release's zips and
#                       SHA256SUMS, such as one `make release DRY_RUN=1`
#                       built, instead of downloading them
#
# Tested on Windows Server 2025 with Windows PowerShell 5.1. Meru never
# runs this script itself and never checks for updates; you run it again
# to update. The script sends nothing. `meru setup` asks whether merud may
# send one anonymous count a day. scripts/install.sh is the Mac's and
# scripts/install-linux.sh Linux's. docs/windows.md has the same steps by
# hand.
#
# The script is ASCII only: Windows PowerShell 5.1 reads a saved .ps1 file
# without a byte-order mark as the system's code page, which garbles any
# other character.

# Everything runs inside Install-Meru. `irm | iex` runs the text in your
# own PowerShell session, where `exit` would close the window, so the
# function returns instead, and its variables stay out of your session.
function Install-Meru {
  # Stop at the first error from a PowerShell command. Native programs
  # such as curl.exe report through $LASTEXITCODE, which the code checks.
  $ErrorActionPreference = 'Stop'
  # Invoke-WebRequest draws a progress bar that slows downloads tenfold in
  # Windows PowerShell 5.1; curl.exe does the downloads, but checks use it.
  $ProgressPreference = 'SilentlyContinue'

  $repo = 'aarora79/meru'
  $installDir = Join-Path $env:LOCALAPPDATA 'Programs\Meru'
  # Meru's search model needs this Ollama or later.
  $minOllama = [version]'0.40.5'
  # NVIDIA's data-center driver for Windows Server 2022 and 2025, the one
  # docs/windows.md installs. Change both together.
  $nvidiaVersion = '596.36'
  $nvidiaFile = "$nvidiaVersion-data-center-tesla-desktop-winserver-2022-2025-dch-international.exe"
  $nvidiaURL = "https://us.download.nvidia.com/tesla/$nvidiaVersion/$nvidiaFile"
  $taskName = 'Meru merud'
  # all is $true when the user answers "install everything" at the start,
  # or sets MERU_YES=1 to skip the question.
  $all = ($env:MERU_YES -eq '1')

  # Say prints a step heading.
  function Say([string]$text) { Write-Host ""; Write-Host "==> $text" -ForegroundColor Cyan }

  # Interactive reports whether there is a person to ask: a console whose
  # input isn't redirected.
  function Interactive { [Environment]::UserInteractive -and -not [Console]::IsInputRedirected }

  # Ask prints a yes/no question and returns $true for yes. With no one to
  # ask, every answer is no unless MERU_YES=1.
  function Ask([string]$question) {
    if ($all) { Write-Host "$question yes"; return $true }
    if (-not (Interactive)) { return $false }
    $reply = Read-Host "$question [y/N]"
    return ($reply -match '^(y|yes)$')
  }

  # Download fetches url into the file out with curl.exe, which Windows 10
  # and later ship, and fails when curl does.
  function Download([string]$url, [string]$out) {
    & curl.exe -fsSL -o $out $url
    if ($LASTEXITCODE -ne 0) { throw "couldn't download $url" }
  }

  # RefreshPath reloads PATH from the machine's and the user's settings,
  # so programs an installer just added are found in this session.
  function RefreshPath {
    $env:Path = [Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' +
      [Environment]::GetEnvironmentVariable('Path', 'User')
  }

  # OllamaExe returns the path of ollama.exe, or $null when Ollama isn't
  # installed. Its installer puts it under %LOCALAPPDATA%\Programs\Ollama.
  function OllamaExe {
    $cmd = Get-Command ollama.exe -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    $p = Join-Path $env:LOCALAPPDATA 'Programs\Ollama\ollama.exe'
    if (Test-Path $p) { return $p }
    return $null
  }

  # OllamaVersion returns the installed Ollama's version, or $null.
  # `ollama --version` prints "ollama version is 0.40.16", sometimes after
  # a warning line, so the last version on its output wins.
  function OllamaVersion {
    $exe = OllamaExe
    if (-not $exe) { return $null }
    $text = (& $exe --version 2>&1 | Out-String)
    $found = [regex]::Matches($text, '\d+\.\d+(\.\d+)?')
    if ($found.Count -eq 0) { return $null }
    return [version]$found[$found.Count - 1].Value
  }

  # OllamaUp waits up to 30 seconds for Ollama's API on loopback, and
  # returns whether it answered.
  function OllamaUp {
    for ($i = 0; $i -lt 30; $i++) {
      try { Invoke-RestMethod -TimeoutSec 2 'http://127.0.0.1:11434/api/version' | Out-Null; return $true } catch { }
      Start-Sleep -Seconds 1
    }
    return $false
  }

  # NvidiaReady reports whether NVIDIA's driver answers, through
  # nvidia-smi, which the driver installs in the system folder.
  function NvidiaReady {
    $smi = Join-Path $env:SystemRoot 'System32\nvidia-smi.exe'
    if (-not (Test-Path $smi)) { return $false }
    & $smi -L 2>&1 | Out-Null
    return ($LASTEXITCODE -eq 0)
  }

  # CheckSigner fails unless file carries a valid Authenticode signature
  # whose signer's organization is org, so a download that someone changed
  # never runs.
  function CheckSigner([string]$file, [string]$org) {
    $sig = Get-AuthenticodeSignature $file
    if ($sig.Status -ne 'Valid' -or $sig.SignerCertificate.Subject -notmatch "O=$([regex]::Escape($org))(,|$)") {
      throw "$(Split-Path $file -Leaf) isn't signed by $org (status $($sig.Status)); the script didn't run it"
    }
  }

  # CheckSums fails unless each of files has the SHA-256 that sums, the
  # text of a SHA256SUMS file, lists for its name. A line reads
  # "<hash>  <name>", or "<hash> *<name>" from shasum's binary mode.
  function CheckSums([string]$sums, [string[]]$files) {
    foreach ($f in $files) {
      $name = Split-Path $f -Leaf
      $line = ($sums -split "`n") | Where-Object { $_ -match "^([0-9a-fA-F]{64}) [ *]$([regex]::Escape($name))\s*$" } | Select-Object -First 1
      if (-not $line) { throw "SHA256SUMS lists no $name; the script installed nothing" }
      $want = ($line -split '\s+')[0].ToLower()
      $got = (Get-FileHash -Algorithm SHA256 $f).Hash.ToLower()
      if ($got -ne $want) { throw "$name doesn't match SHA256SUMS; the script installed nothing" }
    }
  }

  # ---- What this machine has ----

  if ($env:OS -ne 'Windows_NT') { Write-Host 'install.ps1 is for Windows; on a Mac use install.sh, on Linux install-linux.sh.'; return }
  if ($env:PROCESSOR_ARCHITECTURE -ne 'AMD64') { Write-Host "Meru has no build for this processor ($env:PROCESSOR_ARCHITECTURE) yet."; return }
  if (-not (Get-Command curl.exe -ErrorAction SilentlyContinue)) { Write-Host 'curl.exe is missing; it comes with Windows 10 and later.'; return }

  # The release: MERU_FROM's folder, MERU_VERSION, or the latest, which
  # github.com/<repo>/releases/latest redirects to.
  $from = $env:MERU_FROM
  $version = $env:MERU_VERSION
  if ($from) {
    $zip = Get-ChildItem -Path $from -Filter 'meru-v*-windows-amd64.zip' | Select-Object -First 1
    if (-not $zip) { Write-Host "$from holds no meru-<version>-windows-amd64.zip."; return }
    $version = $zip.Name -replace '^meru-(.+)-windows-amd64\.zip$', '$1'
  } elseif (-not $version) {
    $url = & curl.exe -fsSLI -o NUL -w '%{url_effective}' "https://github.com/$repo/releases/latest"
    $version = ($url -split '/')[-1]
  }
  if ($version -notmatch '^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$') { Write-Host "Couldn't find the release to install (got '$version')."; return }
  $base = "https://github.com/$repo/releases/download/$version"
  $cli = "meru-$version-windows-amd64.zip"
  $desktop = "meru-desktop-$version-windows-amd64.zip"

  # The desktop app comes in releases from v0.7.9 on; an older one lacks it.
  if ($from) {
    $hasDesktop = Test-Path (Join-Path $from $desktop)
  } else {
    & curl.exe -fsSLI -o NUL "$base/$desktop" 2>$null
    $hasDesktop = ($LASTEXITCODE -eq 0)
  }
  # The desktop app draws its window with Microsoft's WebView2 runtime,
  # which Windows 11 and recent Windows Server ship.
  $webview = '{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}'
  $hasWebView = (Test-Path "HKLM:\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\$webview") -or
    (Test-Path "HKCU:\Software\Microsoft\EdgeUpdate\Clients\$webview")

  $ollamaVersion = OllamaVersion
  $ollamaOK = $ollamaVersion -and ($ollamaVersion -ge $minOllama)

  # An NVIDIA card shows as a PCI device from vendor 10DE. Before its
  # driver, Windows calls it "3D Video Controller"; its HDMI sound device
  # shares the vendor, so only display devices count.
  $nvidiaCard = Get-PnpDevice -PresentOnly -ErrorAction SilentlyContinue |
    Where-Object { $_.InstanceId -like 'PCI\VEN_10DE*' -and ($_.Class -eq 'Display' -or $_.FriendlyName -like '*Video Controller*') } |
    Select-Object -First 1
  $nvidiaReady = NvidiaReady
  $gpu = ''
  if ($nvidiaReady) {
    $gpu = (& (Join-Path $env:SystemRoot 'System32\nvidia-smi.exe') --query-gpu=name,memory.total --format=csv,noheader 2>$null | Select-Object -First 1)
  }
  # ProductType 1 is a workstation (Windows 10 or 11); 2 and 3 are
  # servers, whose NVIDIA cards are data-center cards like the L40S.
  $isServer = ((Get-CimInstance Win32_OperatingSystem).ProductType -ne 1)
  $isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

  Write-Host ""
  Write-Host "  Meru $version for Windows"
  Write-Host "  ---------------------------"
  Write-Host "  This takes several minutes, most of it downloading models."
  Write-Host "  Here is what it installs:"
  Write-Host ""
  Write-Host "    - meru and merud, about 80 MB, in $installDir, on your PATH"
  if ($hasDesktop) { Write-Host "    - the desktop app, about 25 MB, with a Start menu entry" }
  if ($nvidiaCard -and -not $nvidiaReady) {
    if ($isServer -and $isAdmin) {
      Write-Host "    - NVIDIA's data-center driver $nvidiaVersion, about 700 MB, for the card with no driver"
    } else {
      Write-Host "    - (an NVIDIA card has no driver; the script says how to install one)"
    }
  }
  if ($ollamaOK) { Write-Host "    - Ollama: $ollamaVersion is installed" } else { Write-Host "    - Ollama, from ollama.com, about 1.8 GB" }
  if ($gpu) {
    Write-Host "    - meru setup, which offers the answer model that fits the GPU ($gpu) and downloads the models"
  } else {
    Write-Host "    - meru setup, which offers the answer model that fits this machine and downloads the models"
  }
  Write-Host "    - merud, started now and at each sign-in by a Task Scheduler task"
  Write-Host ""
  Write-Host "  It connects only to GitHub, ollama.com and nvidia.com, to download."
  Write-Host "  meru setup asks whether Meru may send one anonymous count a day."
  Write-Host ""

  if (-not $all -and (Interactive)) {
    $choice = Read-Host 'Install everything [a], choose each step [s], or quit [q]?'
    switch -Regex ($choice) {
      '^[aA]' { $all = $true }
      '^[sS]' { $all = $false }
      default { Write-Host 'Nothing installed.'; return }
    }
  }

  # ---- meru and merud ----

  Say "Installing Meru $version"
  $tmp = Join-Path ([IO.Path]::GetTempPath()) ("meru-install-" + [guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Path $tmp | Out-Null
  # try/finally removes the download folder however the script ends.
  try {
    foreach ($f in @($cli, 'SHA256SUMS')) {
      if ($from) { Copy-Item (Join-Path $from $f) (Join-Path $tmp $f) } else { Download "$base/$f" (Join-Path $tmp $f) }
    }
    $sums = Get-Content -Raw (Join-Path $tmp 'SHA256SUMS')

    Say 'Checking the download against SHA256SUMS'
    CheckSums $sums @((Join-Path $tmp $cli))

    # A running merud or desktop app holds its .exe open, and Windows
    # refuses to replace an open program, so both stop first. The sign-in
    # task starts merud again below.
    $running = Get-Process -Name merud, meru-desktop -ErrorAction SilentlyContinue
    if ($running) {
      Write-Host 'Stopping the running merud and desktop app to replace them.'
      $running | Stop-Process -Force
      Start-Sleep -Seconds 2
    }

    Say "Installing meru and merud in $installDir"
    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    Expand-Archive -Path (Join-Path $tmp $cli) -DestinationPath $tmp -Force
    $unpacked = Join-Path $tmp ($cli -replace '\.zip$', '')
    Copy-Item (Join-Path $unpacked 'meru.exe'), (Join-Path $unpacked 'merud.exe') $installDir -Force
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not (($userPath -split ';') -contains $installDir)) {
      [Environment]::SetEnvironmentVariable('Path', (($userPath.TrimEnd(';') + ';' + $installDir).TrimStart(';')), 'User')
      Write-Host "Added $installDir to your PATH. A terminal opened before now needs reopening to see it."
    }
    RefreshPath
    $meru = Join-Path $installDir 'meru.exe'
    $merud = Join-Path $installDir 'merud.exe'

    # ---- The desktop app ----

    if ($hasDesktop -and (Ask 'Install the desktop app?')) {
      if ($from) { Copy-Item (Join-Path $from $desktop) (Join-Path $tmp $desktop) } else { Download "$base/$desktop" (Join-Path $tmp $desktop) }
      CheckSums $sums @((Join-Path $tmp $desktop))
      Expand-Archive -Path (Join-Path $tmp $desktop) -DestinationPath $tmp -Force
      Copy-Item (Join-Path (Join-Path $tmp ($desktop -replace '\.zip$', '')) 'meru-desktop.exe') $installDir -Force
      # A Start menu shortcut, made through Windows Script Host's COM
      # object, the usual way a script makes a .lnk file.
      $lnk = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\Meru.lnk'
      $shortcut = (New-Object -ComObject WScript.Shell).CreateShortcut($lnk)
      $shortcut.TargetPath = Join-Path $installDir 'meru-desktop.exe'
      $shortcut.WorkingDirectory = $installDir
      $shortcut.Description = 'Meru, a personal AI assistant on your own computer'
      $shortcut.Save()
      Write-Host 'Meru is in the Start menu.'
      if (-not $hasWebView) {
        Write-Host 'The desktop app needs Microsoft Edge WebView2, which this machine lacks.'
        Write-Host 'Install it from https://developer.microsoft.com/microsoft-edge/webview2/'
      }
    } elseif (-not $hasDesktop) {
      Write-Host "Release $version has no Windows desktop app; use meru and meru chat."
    }

    # ---- NVIDIA's driver ----

    if ($nvidiaCard -and -not $nvidiaReady) {
      Say 'An NVIDIA card has no driver'
      Write-Host 'Without it, Ollama runs the models on the processor, which is much slower.'
      if ($isServer -and $isAdmin) {
        if (Ask "Install NVIDIA's data-center driver $nvidiaVersion (about 700 MB)?") {
          $exe = Join-Path $tmp $nvidiaFile
          Download $nvidiaURL $exe
          CheckSigner $exe 'NVIDIA Corporation'
          # -s installs without prompts; -noreboot leaves a restart to you.
          # WaitForExit waits for the installer itself, not for programs it
          # leaves running.
          $p = Start-Process -FilePath $exe -ArgumentList '-s', '-noreboot', '-noeula', '-clean' -PassThru
          $p.WaitForExit()
          if (NvidiaReady) { Write-Host "NVIDIA's driver works." } else { Write-Host "NVIDIA's installer ended with code $($p.ExitCode); restart Windows, then run nvidia-smi." }
        }
      } elseif ($isServer) {
        Write-Host 'Run this script in an administrator PowerShell to install the driver, or see docs/windows.md.'
      } else {
        Write-Host 'Install the driver for your card from https://www.nvidia.com/Download/index.aspx, then run this script again.'
      }
    }

    # ---- Ollama ----

    Say 'Ollama runs the models'
    if ($ollamaOK) {
      Write-Host "Ollama $ollamaVersion is installed."
    } else {
      if ($ollamaVersion) { Write-Host "Ollama $ollamaVersion is too old for Meru's search model; Meru needs $minOllama or later." }
      if (Ask 'Install Ollama from ollama.com (about 1.8 GB)?') {
        $exe = Join-Path $tmp 'OllamaSetup.exe'
        Download 'https://ollama.com/download/OllamaSetup.exe' $exe
        CheckSigner $exe 'Ollama Inc.'
        # Ollama's installer installs for this user, needs no administrator
        # and starts Ollama when it ends. WaitForExit waits for the
        # installer only; Start-Process -Wait would wait for Ollama too.
        $p = Start-Process -FilePath $exe -ArgumentList '/VERYSILENT', '/NORESTART', '/SUPPRESSMSGBOXES' -PassThru
        $p.WaitForExit()
        RefreshPath
        $ollamaVersion = OllamaVersion
        if ($ollamaVersion) { Write-Host "Ollama $ollamaVersion is installed." } else { Write-Host "Ollama's installer ended with code $($p.ExitCode)." }
      } else {
        Write-Host 'Install Ollama from https://ollama.com/download, then run meru setup.'
      }
    }
    $ollama = OllamaExe
    # Ollama's tray app starts its server; if nothing answers, start one.
    if ($ollama -and -not (OllamaUp)) {
      $app = Join-Path (Split-Path $ollama) 'ollama app.exe'
      if (Test-Path $app) { Start-Process $app } else { Start-Process $ollama -ArgumentList 'serve' -WindowStyle Hidden }
      if (-not (OllamaUp)) { Write-Host "Ollama isn't answering on http://127.0.0.1:11434; start the Ollama app, then run meru setup." }
    }

    # ---- meru setup ----

    Say 'Setting up Meru'
    $configPath = Join-Path $env:USERPROFILE '.meru\config.toml'
    if ((Interactive) -and $ollama -and (Ask 'Run meru setup now (models, folders, and questions about you)?')) {
      # meru setup asks its own questions: the answer model that fits this
      # machine, your folders, web search and about you, and downloads the
      # models it picks.
      & $meru setup
      if ($LASTEXITCODE -ne 0) { Write-Host "meru setup stopped; run 'meru setup' again when you're ready." }
    } elseif ($ollama -and -not (Test-Path $configPath) -and (Ask 'Download the lite models and write a default config?')) {
      # No one to ask, as with MERU_YES=1 and no console: the lite
      # profile's two models and the config template, which merud starts
      # with. The template leaves the daily count unchosen, so merud sends
      # nothing until meru chat or the app asks and the user says yes.
      & $ollama pull 'hf.co/openbmb/MiniCPM5-2B-GGUF:Q4_K_M'
      $okFast = ($LASTEXITCODE -eq 0)
      & $ollama pull 'embeddinggemma-2:440m'
      if ($okFast -and $LASTEXITCODE -eq 0) {
        New-Item -ItemType Directory -Force -Path (Split-Path $configPath) | Out-Null
        # meru prints the template in UTF-8; Out-File with ascii would
        # garble it, and Set-Content would add a byte-order mark that the
        # TOML parser rejects, so .NET writes the bytes as they are.
        $template = (& $meru config template | Out-String)
        [IO.File]::WriteAllText($configPath, $template, (New-Object Text.UTF8Encoding $false))
        Write-Host "Wrote $configPath. Run 'meru setup' in a terminal to pick a larger answer model."
      } else {
        Write-Host "The models didn't download; run 'meru setup' once Ollama runs."
      }
    } else {
      Write-Host "Run 'meru setup' when you're ready."
    }

    # ---- merud at sign-in ----

    if (Ask 'Start merud now and at each sign-in, with a Task Scheduler task?') {
      # conhost --headless runs merud, a console program, with no window.
      # The task runs as you, only while you're signed in, with your
      # rights, not an administrator's.
      $user = "$env:USERDOMAIN\$env:USERNAME"
      $action = New-ScheduledTaskAction -Execute (Join-Path $env:SystemRoot 'System32\conhost.exe') -Argument "--headless `"$merud`"" -WorkingDirectory $installDir
      $trigger = New-ScheduledTaskTrigger -AtLogOn -User $user
      # No time limit: the default stops a task after three days. Restart
      # covers a start that fails.
      $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) `
        -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
      $principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
      try {
        Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Settings $settings -Principal $principal `
          -Description 'Starts merud, the Meru daemon, when you sign in.' -Force | Out-Null
        Start-ScheduledTask -TaskName $taskName
        $up = $false
        for ($i = 0; $i -lt 30; $i++) {
          & $meru ping 2>&1 | Out-Null
          if ($LASTEXITCODE -eq 0) { $up = $true; break }
          Start-Sleep -Seconds 1
        }
        if ($up) { Write-Host 'merud is up, and starts at each sign-in.' } else { Write-Host "merud hasn't answered yet; see $env:USERPROFILE\.meru\merud.log" }
      } catch {
        Write-Host "Windows refused the task: $($_.Exception.Message)"
        Write-Host "Start merud yourself with: Start-Process merud -WindowStyle Hidden"
      }
    } else {
      Write-Host 'Start merud yourself with: Start-Process merud -WindowStyle Hidden'
    }

    # ---- Web search ----

    # merud runs SearXNG, the search engine behind web_search, in Docker or
    # Podman, which on Windows need WSL2 and a machine that allows virtual
    # machines. A SearXNG that already answers on Meru's address needs
    # neither.
    $searxng = $false
    try { Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 'http://127.0.0.1:8888/healthz' | Out-Null; $searxng = $true } catch { }
    if (-not $searxng -and -not (Get-Command docker.exe, podman.exe -ErrorAction SilentlyContinue)) {
      Say 'Web search'
      Write-Host 'Web search runs SearXNG in Docker or Podman, and neither is installed. Meru works without it.'
      Write-Host "To add it: Docker Desktop or Podman Desktop with WSL2, or SearXNG on Python, as in"
      Write-Host "https://github.com/$repo/blob/main/docs/windows.md"
    }

    Say 'Done'
    Write-Host 'Ask a question:   meru "what can you do?"'
    Write-Host 'Chat:             meru chat'
    if (Test-Path (Join-Path $installDir 'meru-desktop.exe')) { Write-Host 'Desktop app:      Meru in the Start menu' }
    Write-Host "Gmail, Calendar and Drive: https://github.com/$repo/blob/main/docs/google-setup.md"
  } finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
  }
}

# A failed step throws; this prints its reason in one line, without
# PowerShell's stack of positions, and leaves your session open.
try {
  Install-Meru
} catch {
  Write-Host "install.ps1: $($_.Exception.Message)" -ForegroundColor Red
}
