# Meru on Windows

Meru has no Windows installer yet, and nobody has run it end to end on
Windows. [Issue #85](https://github.com/aarora79/meru/issues/85) tracks the
work. This page records the steps that worked so far, so the installer can
repeat them.

The steps ran on an Amazon EC2 `g6e.xlarge`: Windows Server 2025 Datacenter,
an AMD EPYC with 4 threads, 32 GB of memory and an NVIDIA L40S with 48 GB of
graphics memory.

## 1. Install the NVIDIA driver

Ollama runs a model on an NVIDIA card only through NVIDIA's driver. Without
it, Windows shows the card in Device Manager as "3D Video Controller" with an
error, and Ollama falls back to the CPU, which runs even the `lite` model
slowly.

Check for the card and its driver in PowerShell:

```powershell
# VEN_10DE is NVIDIA's vendor ID. Status "Error" means no driver.
Get-PnpDevice | Where-Object InstanceId -like 'PCI\VEN_10DE*' |
  Select-Object Status, FriendlyName
```

A data-center card such as the L40S, A10G or L4 takes NVIDIA's data-center
driver. Its Windows Server package covers Server 2022 and 2025. We used
596.36, the newest release in
[NVIDIA's data-center driver notes](https://docs.nvidia.com/datacenter/tesla/tesla-release-notes-595-71-05/index.html)
in November 2026. Download it, check that NVIDIA signed it, and install it
without prompts. The commands need an administrator PowerShell:

```powershell
$v   = '596.36'
$dir = "$env:USERPROFILE\Downloads\nvidia-$v"
$exe = "$dir\$v-data-center-tesla-desktop-winserver-2022-2025-dch-international.exe"
New-Item -ItemType Directory -Force $dir | Out-Null
curl.exe -fsSL -o $exe "https://us.download.nvidia.com/tesla/$v/$(Split-Path $exe -Leaf)"

# Stop unless the status is Valid and the signer is NVIDIA Corporation.
Get-AuthenticodeSignature $exe | Format-List Status, SignerCertificate

# -s installs without prompts; -noreboot leaves the restart to you.
Start-Process $exe -ArgumentList '-s','-noreboot','-noeula','-clean' -Wait
```

The file is about 700 MB, and the install takes a few minutes. On the EC2
machine the driver worked at once, with no restart. If `nvidia-smi` fails,
restart Windows and try again:

```powershell
nvidia-smi --query-gpu=name,driver_version,memory.total,driver_model.current --format=csv
# NVIDIA L40S, 596.36, 46068 MiB, MCDM
```

MCDM is the driver mode Windows uses for a compute card with no display.
On a desktop graphics card, such as a GeForce or
an RTX workstation card, install NVIDIA's ordinary display driver instead.

## 2. Build Meru from source

Releases carry `meru.exe` and `merud.exe`, but until #85 closes, build from
source to get the fixes on the `windows-support` branch. Go needs no
installer: unpack its zip into your user folder and put its `bin` folder
first on `PATH` in each shell.

```powershell
$go = "$env:USERPROFILE\sdk"
New-Item -ItemType Directory -Force $go | Out-Null
curl.exe -fsSL -o "$go\go.zip" https://go.dev/dl/go1.26.11.windows-amd64.zip
# Compare this hash with the one on https://go.dev/dl/ before you unpack.
Get-FileHash "$go\go.zip" -Algorithm SHA256
Expand-Archive "$go\go.zip" -DestinationPath $go
$env:PATH = "$go\go\bin;$env:PATH"
go build ./cmd/meru ./cmd/merud
```

The Go version must match the `toolchain` line in `go.mod`. The tests run
with `go test ./...`. The `-race` flag needs a C compiler on Windows, so
leave it off unless you have one.

Git for Windows turns on `core.autocrlf`, which checks text files out with
Windows line endings. Go compiles the config template, the skills and the
desktop app's page into the binaries, so a build from such a checkout
carries those line endings, and `gofmt` flags every Go file. Clone with the
setting off:

```powershell
git clone -c core.autocrlf=false https://github.com/aarora79/meru.git
```
