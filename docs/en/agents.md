[Português](../agentes.md) | English

# Agents

The agent (`invenire-agent`) runs as a service on each inventoried machine: it collects hardware,
operating system and installed software at startup and then every 4 hours, keeps the results in a
local queue and sends them to the server. There are agents for **Windows** (amd64) and **Linux**
with systemd (amd64 and arm64).

The server distributes the agent: the Docker image includes the executables with `SHA256SUMS`,
and the dashboard builds the installer package from them. No one needs Go or a network share.

## Installing

Everything starts in **Adicionar máquinas** (Add machines), in the dashboard: create an
enrollment token (unit, how many machines, validity). The screen shows the token once and three
ways to install.

### 1. Package to download (the easiest)

A `.zip` (Windows) or `.tar.gz` (Linux) built on the spot, with the agent, the installer,
`SHA256SUMS` and an `instalar.conf` with the server address, the pin (if any) and the token.
There is nothing to type, it works with or without a pin and without internet on the machine
(via USB stick, for example).

**Windows:** right-click the `.zip` > **Extract All**; in the `invenire-agente` folder,
double-click `Instalar.cmd` and answer **Yes** when Windows asks for administrator permission.
`Instalar.cmd` calls the `instalar.ps1` in the same folder with `-ExecutionPolicy Bypass`.

**Linux:**

```sh
tar xzf invenire-agente-linux.tar.gz
sudo ./invenire-agente/instalar.sh
```

The Linux package includes both the amd64 and arm64 agents; the installer picks by processor.

After installing, if the token was for a single machine, the installer deletes `instalar.conf`
(which holds the token). If the token can still enroll others, the same package serves them all:
the installer says so, and you delete the package when the batch is done. **The package contains
the token**: treat it like a password, with a short validity and few uses, and revoke the token
in the dashboard if it gets lost.

### 2. One-line command

Only when the server has a public certificate ([VPS](install/vps.md) or
[reverse proxy](install/reverse-proxy.md)): with a pin, a command-line download would have no way
to check the server, and the scripts refuse to download the agent. The token screen shows the
command ready to use:

```sh
curl -fsSL https://invenire.exemplo.org/download/instalar.sh | sudo INVENIRE_ENROLL_TOKEN=inv_… sh
```

On Windows, in PowerShell **as administrator**:

```powershell
$env:INVENIRE_ENROLL_TOKEN = "inv_…"; [Net.ServicePointManager]::SecurityProtocol = 'Tls12'; & ([scriptblock]::Create((irm https://invenire.exemplo.org/download/instalar.ps1)))
```

The token goes in an environment variable, never in the URL (which would end up in the proxy's
log). The served script already contains the server address, downloads the agent from
`/download/agent/…`, checks it against `/download/SHA256SUMS` and installs it.

### 3. Manual

For those who want to see each step, with the executables from a release folder (produced by
`scripts/build.sh`, see [development.md](development.md#building-a-release)) or downloaded from
`/download/agent/windows-amd64`, `/download/agent/linux-amd64` or `/download/agent/linux-arm64`.
As administrator (Windows) or root (Linux):

```sh
invenire-agent enroll --server https://192.168.0.10:8443 --ca-pin sha256:… --token inv_…
invenire-agent service install
```

Without `--ca-pin` when the server has a public certificate. The token can also go in
`INVENIRE_ENROLL_TOKEN`, so it stays out of the shell history and the process list.

The scripts in the release folder do the same, checking the executable first:

```powershell
$env:INVENIRE_ENROLL_TOKEN = "inv_…"
.\instalar.ps1 -Server https://192.168.0.10:8443 -CaPin sha256:…
```

```sh
sudo INVENIRE_ENROLL_TOKEN=inv_… ./instalar.sh --server https://192.168.0.10:8443 --ca-pin sha256:…
```

Script options: `-Server`/`--server`, `-CaPin`/`--ca-pin`, `-Agent`/`--agent` (path to the
executable), `-Interval`/`--interval` (default `4h`) and `-Force`/`--force` (enrolls again and
revokes the machine's previous badge). Where each value comes from, in this order: the parameter;
the `instalar.conf` next to the script; what the server filled into the script it serves.

### Windows: SmartScreen and antivirus

The executables do not have code signing yet. When opening an `Instalar.cmd` downloaded from the
internet, Windows may show **"Windows protected your PC"**: click **More info** and **Run
anyway**. Some antivirus may block `invenire-agent.exe`; in that case, allow the file in the
antivirus. The scripts check the executable against `SHA256SUMS` before running it: a modified
executable is rejected.

## Checking

The installer ends with `pronto: serviço invenire-agent ativo; a primeira coleta já foi enviada
ou está na fila` (done: invenire-agent service active; the first collection has been sent or is
queued). Within a few seconds the machine shows up in **Visão geral** (Overview) as `em-dia` (up
to date). If it shows another state, see [troubleshooting.md](troubleshooting.md).

| | Windows | Linux |
|---|---|---|
| Executable | `%ProgramFiles%\Invenire\invenire-agent.exe` | `/usr/local/bin/invenire-agent` |
| Service | `invenire-agent`, automatic (delayed start), SYSTEM account | `/etc/systemd/system/invenire-agent.service`, root, enabled |
| Is it running | `Get-Service invenire-agent` | `systemctl status invenire-agent` |
| Log | Event Viewer > Application, source `invenire-agent` | `journalctl -u invenire-agent` |
| State (key, badge, queue) | `C:\ProgramData\Invenire` | `/var/lib/invenire` |
| If the process dies | restarts after 1, 5 and 15 min | restarts after 60 s |
| Badge revoked (exit 3) | stops and does not restart | stops and does not restart (`RestartPreventExitStatus`) |

The systemd unit runs as root, but with the system protected: `ProtectSystem=full`,
`ProtectHome=read-only`, `NoNewPrivileges`, no access to devices, kernel modules or tunables, and
only IP and Unix sockets. It is generated from
`cmd/invenire-agent/invenire-agent.service.tmpl`.

## Updating

Run the installer again, with the new version: it sees the machine is already enrolled, skips
enrollment and replaces the service's executable. No token needed.

- Linux, with a public certificate:
  `curl -fsSL https://invenire.exemplo.org/download/instalar.sh | sudo sh` (the server delivers
  the agent of its own version).
- With the package: download a new package from the dashboard after updating the server (the
  dashboard only builds a package for a valid token; a single-use, short-lived token works, and it
  is not consumed by a machine that is already enrolled) and run its `Instalar.cmd` or
  `instalar.sh`.
- With the release folder: run its `instalar.ps1` or `instalar.sh`.

Under the hood, it is `invenire-agent service install` with the new executable: it stops the
service, replaces the executable and the configuration and starts it again.

## Changing the server

When the server changes address (from local network to proxy, or to another machine with the same
organization), the agent switches address without re-enrolling. As administrator or root:

```sh
invenire-agent set-server --server https://invenire.exemplo.org                       # public certificate
invenire-agent set-server --server https://192.168.0.20:8443 --ca-pin sha256:…       # with a pin
```

On Windows, the executable is `& "$env:ProgramFiles\Invenire\invenire-agent.exe"`. Before saving,
`set-server` asks the new server (`GET /v1/agent`) whether it recognizes the badge; if it does
not, or if the pin does not match, nothing changes. Then restart the service
(`Restart-Service invenire-agent` or `systemctl restart invenire-agent`).

The badge comes from the organization's CA, which travels with the organization (backup and
restore, or export with the CA key): that is why the same badge is valid on the new server.

## Uninstalling

Windows (PowerShell as administrator):

```powershell
& "$env:ProgramFiles\Invenire\invenire-agent.exe" service uninstall
```

Linux:

```sh
sudo /usr/local/bin/invenire-agent service uninstall
```

The executable and the state (`C:\ProgramData\Invenire` or `/var/lib/invenire`) are left behind;
delete the state folder to get rid of the badge. In the dashboard, in **Máquinas** (Machines),
revoke the agent so the server stops accepting it.

## How the agent works

**Collecting without a server**, to check what the agent sees or for isolated networks:

```sh
invenire-agent collect --out snapshot.json
```

On Linux, run it as root: `product_uuid` and `product_serial` are readable only by root.

**Enrolling and sending**, by hand (the service runs `run`):

```sh
invenire-agent enroll --server https://invenire.local:8443 --token inv_… --ca-pin sha256:…
invenire-agent run               # collects every 4h (--interval) and sends
invenire-agent run --once        # one collection and one send attempt
```

On first contact, with a pin, the agent only accepts the server if the TLS chain contains the CA
with that SHA-256; after that, it trusts only that CA. Without a pin, it checks the server against
the system CAs, like a browser. The agent requires TLS 1.3.

**State.** In `%ProgramData%\Invenire` or `/var/lib/invenire` (`--state-dir`): key, certificate
(the badge), CA, `agent.json`, the hash of the last snapshot (for `prev_sha256`) and the queue in
`queue/`. On Windows the directory is owned by Administrators and accessible only to SYSTEM and
Administrators; on Linux, 0700.

**Queue.** Holds up to 100 snapshots (`--max-queue`) with the exact bytes that will be sent; each
file name carries the SHA-256, checked before sending. What the server rejects for good goes to
`queue/rejected/` with the reason. If the server rejects the badge itself (agent revoked or
unknown, badge expired), `run` stops with exit code 3 and the queue is left intact. Another
`401`, and a `413` that does not come from the server itself, usually come from a proxy along the
way: the snapshot stays in the queue and the agent tries again, with increasing backoff.

**Authentication.** The agent presents the badge in TLS (mTLS) and, in addition, signs every
request with its key (`Invenire-Agent-Cert`, `Invenire-Timestamp`, `Invenire-Signature`). The
signature is what gets through a proxy that terminates TLS; see
[security.md](security.md#the-agent-and-the-server).

**Renewal.** The badge is valid for two years and the agent renews it by itself when eight months
are left (`POST /v1/agent/renew`), with the same key. If renewal fails, the current badge stays in
use and the next collection tries again. Only a machine left switched off until the badge expired
needs an `enroll --force`.

**Service options:**

```sh
invenire-agent service install                 # --interval, --state-dir, --max-queue, --bin
invenire-agent service uninstall               # executable and state are left behind
```

`install` copies the executable to a place only administrators can change (`--bin`), registers the
`invenire-agent` service running `run` with the given options and starts it.
