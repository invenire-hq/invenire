[Português](../../instalar/rede-local.md) | English

# Installing on a local network

For when the inventoried machines and the server are on the same network (or a VPN) and there is
no domain or public certificate. The server serves HTTPS directly on port 8443, with a
certificate issued by the installation's own CA; each agent recognizes the server by the **pin**
of that CA, which is included in the installer package.

Not your case? See [which scenario to choose](choosing.md).

Time: about 20 minutes, most of it waiting for the image to build.

## What you need

- A Linux machine for the server, always on, with **Docker Engine** and the **compose** plugin
  (`docker compose version` answers) and **git**.
- The machine's **fixed IP** on the network (or an internal DNS name pointing to it). The
  machines will call the server at that address, and it goes into the certificate: an address
  not on the list is rejected by the agent.
- Port **8443/tcp** open in the server machine's firewall to the internal network.
- A browser on a machine on the network, for the dashboard.

In the examples, the server is `192.168.0.10` and is named `invenire.local`. Replace them with
yours.

## 1. Download Invenire

On the server machine:

```sh
sudo git clone https://github.com/invenire-hq/invenire.git /opt/invenire
cd /opt/invenire/deploy/servidor
```

## 2. Configure

```sh
sudo cp .env.example .env
openssl rand -hex 24
sudo nano .env
```

In `.env`, set the following (the rest can stay as it is):

```ini
COMPOSE_FILE=compose.yml
INVENIRE_PG_PASSWORD=<the password openssl generated>
INVENIRE_HOSTS=localhost,invenire.local,192.168.0.10
```

`INVENIRE_HOSTS` are **all** the names and IPs by which anyone will call the server: the machines
and also the dashboard's browser. If you want to add one later, change it here and start again
(step 3); the certificate is reissued on every start.

Keep the database password with the backups: `.env` does not go into git.

## 3. Start

```sh
sudo docker compose up -d --wait
```

The first time, it downloads the images, including the server's
(`ghcr.io/invenire-hq/invenire-server`, which already includes the agent for Windows and Linux).
To build the image from source, use `--build` (a few minutes). PostgreSQL and the server come
back by themselves when the machine reboots.

**Check.** Both services show as `healthy`:

```sh
sudo docker compose ps
```

And the server says it is up ("no ar"), with the CA pin:

```sh
sudo docker compose logs server | grep "no ar"
```

```
server-1  | {"time":"…","level":"INFO","msg":"invenire-server no ar","version":"dev","listen":":8443","tls":"internal","ca_sha256":"5d75…f345"}
```

From the machine where you will open the browser, check that the port answers (`-k` because the
certificate comes from the installation's CA, which `curl` does not know):

```sh
curl -k -o /dev/null -w '%{http_code}\n' https://192.168.0.10:8443/healthz
```

It must show `204`. No answer means firewall or address.

## 4. First-time setup in the dashboard

Get the first-time setup code:

```sh
sudo docker compose logs server | grep "primeira configuração"
```

```
server-1  | {"time":"…","level":"WARN","msg":"primeira configuração: abra o painel no navegador (/setup) e use este código","codigo":"WGQS-XWWH-TULK"}
```

Open in the browser **the same address the machines will use**, for example
`https://192.168.0.10:8443/`. The dashboard builds the installer package with the address you
opened it at: opened via `localhost`, the package would send the machines to `localhost`.

The browser will warn that the connection is not secure: it does not know the installation's CA.
That is expected in this scenario. Click **Advanced** and **Continue to 192.168.0.10**. If a
window appears asking you to choose a certificate (common with a Brazilian e-CPF or e-CNPJ
digital certificate installed), click **Cancel**: the server asks for a certificate only because
of the agents.

The address leads to **Primeira configuração** (First-time setup). Fill in the code, the
organization name and the first administrator (name, email and password) and click **Criar
organização** (Create organization).

**Check.** You land, logged in, on the **Adicionar máquinas** (Add machines) screen, with "Tudo
pronto para começar" (All set to start). If the code is not accepted, check that you copied the
most recent log line (each start without an organization generates a new code).

## 5. Enrollment token and installer package

Still in **Adicionar máquinas**:

1. (Optional) Create a unit so machines land in the right place right away, for example "Sede"
   (headquarters). You can do it later, in **Unidades** (Units).
2. In **Novo token de cadastro** (New enrollment token), choose the unit, **Quantas máquinas**
   (how many machines) the token enrolls and the **Validade** (validity), and click **Criar
   token** (Create token).

The screen shows the token (only this once), the **Endereço do servidor** (server address) and
the **Pin da CA** (CA pin).

**Check.** The address is `https://192.168.0.10:8443` (the one the machines reach) and the pin is
the same `ca_sha256` from the "no ar" log line (step 3), prefixed with `sha256:`.

In **1. Pacote para baixar** (package to download), download the package for the machine's
system:

- **Windows:** `Baixar pacote para Windows (.zip)`;
- **Linux:** `Baixar pacote para Linux (.tar.gz)`.

The package contains the agent, the installer and an `instalar.conf` with the address, pin and
token. Treat it like a password. **2. Comando de uma linha** (one-line command) shows as "Não
disponível neste servidor" (not available on this server): with a pin, a command-line download
would have no way to check the server.

## 6. Install on the first machine

Take the package to the machine (shared folder, USB stick, `scp`).

**Windows:** right-click the `.zip` > **Extract All**, go into the `invenire-agente` folder and
double-click `Instalar.cmd`. Answer **Yes** when Windows asks for administrator permission. If
"Windows protected your PC" appears, click **More info** and **Run anyway** (the executables do
not have code signing yet; see
[agents.md](../agents.md#windows-smartscreen-and-antivirus)).

**Linux** (with systemd, amd64 or arm64):

```sh
tar xzf invenire-agente-linux.tar.gz
sudo ./invenire-agente/instalar.sh
```

**Check.** The installer ends with:

```
pronto: serviço invenire-agent ativo; a primeira coleta já foi enviada ou está na fila
```

("done: invenire-agent service active; the first collection has been sent or is queued"). On
Windows the window shows "Pronto. Pode fechar esta janela." (Done. You can close this window.).
If the token was for a single machine, the installer deletes `instalar.conf` and says so.

On the machine, the service is running:

```sh
systemctl status invenire-agent          # Linux
```

```powershell
Get-Service invenire-agent               # Windows, in PowerShell
```

## 7. See the machine `em-dia`

Go back to the dashboard, to **Visão geral** (Overview). The machine shows up within a few
seconds with the state `em-dia` (up to date) (the page refreshes itself every minute; reload to
see it sooner). In **Máquinas** (Machines), click it to see the installed software.

From the command line, on the server:

```sh
sudo docker compose exec server invenire-server status --tenant "Minha Organização"
```

Replace "Minha Organização" with the name you gave in step 4. The machine shows up as `em-dia`.

If it does not show up, or shows up with another state, see
[troubleshooting.md](../troubleshooting.md).

## Next

- **Backup**, from day one: [operations.md](../operations.md#backup). Without a backup of the data
  folder (CA and master key), a lost server forces you to re-enroll every machine.
- **More machines**, agent updates and uninstalling: [agents.md](../agents.md).
- **Other people in the dashboard**: in **Acesso** (Access), create users (`read` only views;
  `admin` can also change things).
- **A certificate without browser warnings**: with a domain, move to a
  [reverse proxy](reverse-proxy.md). The server stops serving on port 8443, and each agent already
  enrolled needs to be pointed to the new address, without re-enrolling: see
  [agents.md](../agents.md#changing-the-server).
