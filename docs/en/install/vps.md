[Português](../../instalar/vps.md) | English

# Installing on a VPS

For when the server is on the internet, on a VPS (or any machine with a public IP), and the
inventoried machines reach it from wherever they are. A **Caddy** runs in front of
`invenire-server` and obtains and renews the Let's Encrypt certificate by itself: you only give
it the domain. The server speaks plain HTTP only with Caddy, over the compose internal network,
and the agent authenticates by signing every request.

Already have an Nginx or another proxy on that machine? Use the
[reverse proxy](reverse-proxy.md) guide. Not sure which case is yours? See
[choosing.md](choosing.md).

## What you need

- A Linux VPS (1 vCPU and 1 GB of memory are enough to start), with **Docker Engine**, the
  **compose** plugin (`docker compose version` answers) and **git**. To install Docker, follow
  Docker's official documentation for your distribution.
- A **domain** (or subdomain) just for Invenire, with an **A** record (and **AAAA**, if the VPS
  has IPv6) pointing to the VPS's IP. In the examples, `invenire.exemplo.org`.
- Ports **80** and **443/tcp** open to the internet, in the provider's firewall and the VPS's.
  Port 80 is how Let's Encrypt validates the domain; Caddy redirects everything else to HTTPS.
- Nothing else listening on the VPS's ports 80 and 443.

**Check before you start.** The domain already points to the VPS:

```sh
getent hosts invenire.exemplo.org
```

It must show the VPS's IP. If you have just created the record, it may take a few minutes.

## 1. Download Invenire

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

In `.env`:

```ini
COMPOSE_FILE=compose.caddy.yml
INVENIRE_PG_PASSWORD=<the password openssl generated>
INVENIRE_DOMAIN=invenire.exemplo.org
```

(Remove the `#` in front of `INVENIRE_DOMAIN`.)

## 3. Start

```sh
sudo docker compose up -d --wait
```

The first time, it downloads the images (`--build` builds the server's from source). PostgreSQL,
the server and Caddy come back by themselves when the VPS reboots.

**Check.** `sudo docker compose ps` shows the three services up (`postgres` and `server` as
`healthy`). Caddy obtains the certificate right after starting:

```sh
sudo docker compose logs caddy | grep "certificate obtained successfully"
```

If it does not appear within a minute, look at the errors with `sudo docker compose logs caddy`:
it is almost always the domain not pointing to the VPS or port 80 closed.

From any machine:

```sh
curl -s -o /dev/null -w '%{http_code}\n' https://invenire.exemplo.org/healthz
```

It must show `204`, with no certificate error.

## 4. First-time setup in the dashboard

Get the code:

```sh
sudo docker compose logs server | grep "primeira configuração"
```

Do this right away: until first-time setup is done, whoever has the code can create the
organization, and it only appears in the VPS's log.

Open `https://invenire.exemplo.org/` in the browser, with no certificate warning. It leads to
**Primeira configuração** (First-time setup): fill in the code, the organization name and the
first administrator and click **Criar organização** (Create organization).

**Check.** You land, logged in, on **Adicionar máquinas** (Add machines), with "Tudo pronto para
começar" (All set to start).

## 5. Enrollment token

In **Adicionar máquinas**, create a unit (if you want), and in **Novo token de cadastro** (New
enrollment token) choose the unit, **Quantas máquinas** (how many machines) and the **Validade**
(validity), and click **Criar token** (Create token).

**Check.** The **Endereço do servidor** (server address) is `https://invenire.exemplo.org` and
the **Pin da CA** (CA pin) says "nenhum: o servidor usa certificado público" (none: the server
uses a public certificate).

## 6. Install on the first machine

**One-line command** (in **2. Comando de uma linha**, on the token screen). On Linux:

```sh
curl -fsSL https://invenire.exemplo.org/download/instalar.sh | sudo INVENIRE_ENROLL_TOKEN=inv_… sh
```

On Windows, the command on the screen, in PowerShell **as administrator**.

Or the **package to download**, as on the
[local network](local-network.md#6-install-on-the-first-machine): a `.zip` with `Instalar.cmd`
on Windows, a `.tar.gz` with `instalar.sh` on Linux.

**Check.** The installer ends with
`pronto: serviço invenire-agent ativo; a primeira coleta já foi enviada ou está na fila`.

## 7. See the machine `em-dia`

In the dashboard, in **Visão geral** (Overview), the machine shows up within a few seconds as
`em-dia` (up to date). From the command line, in the `deploy/servidor` folder:

```sh
sudo docker compose exec server invenire-server status --tenant "Minha Organização"
```

If it does not show up, see [troubleshooting.md](../troubleshooting.md).

## Next

- **Backup**, to somewhere off the VPS: [operations.md](../operations.md#backup). Caddy's
  certificate is not part of the backup and does not need to be: on a new VPS, Caddy obtains
  another one by itself.
- **More machines**, agent updates and uninstalling: [agents.md](../agents.md).
- The dashboard is exposed to the internet: use strong passwords and give `admin` only to those
  who need it ([security.md](../security.md)).

## How Caddy is configured

`deploy/servidor/caddy/Caddyfile`:

```caddy
{$INVENIRE_DOMAIN} {
	request_body {
		max_size 32MB
	}
	reverse_proxy server:8080 {
		transport http {
			read_timeout 5m
			write_timeout 5m
		}
	}
}
```

Caddy already does what Nginx needs several lines for: HTTP to HTTPS, certificate and renewal,
TLS 1.3, `Host`, `X-Forwarded-For` (discarding whatever the client sent) and
`X-Forwarded-Proto`. The server accepts its headers because Caddy connects from inside the
compose network (`--trusted-proxies 172.31.250.0/24`). The body limit and timeouts are the same
as in the [Nginx recipe](reverse-proxy.md#5-nginx).
