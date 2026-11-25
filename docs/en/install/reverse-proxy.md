[Português](../../instalar/proxy-reverso.md) | English

# Installing behind a reverse proxy

For when there already is (or will be) a reverse proxy in front, with a domain and a public
certificate: the organization's Nginx, an Nginx Proxy Manager, a Traefik. The proxy terminates
TLS and forwards to `invenire-server` over plain HTTP, on a port only it can reach. The agent
proves who it is by **signing every request** with its badge key, so the proxy does not need to
forward a client certificate or have any special configuration.

The recipe below is for **Nginx** with a Let's Encrypt certificate (certbot), on the same machine
as the server. For other proxies, see the [compatibility matrix](#other-proxies). With no proxy
at all and a fresh VPS, the shortest path is the [VPS with Caddy](vps.md).

Not your case? See [which scenario to choose](choosing.md).

## What you need

- A Linux machine with **Docker Engine** and the **compose** plugin (`docker compose version`
  answers), **git** and **Nginx**.
- A **domain** (or subdomain) just for Invenire, pointing to the Nginx machine. In the examples,
  `invenire.exemplo.org`. A subpath (`exemplo.org/invenire/`) does not work: the dashboard and
  the installers use paths from the root.
- Ports **80** and **443** open to whoever will use it: the inventoried machines and the
  dashboard's browser (and, for certbot, the internet).

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
COMPOSE_FILE=compose.proxy.yml
INVENIRE_PG_PASSWORD=<the password openssl generated>
```

With Nginx on this same machine, that is all: the server listens on `127.0.0.1:8080`, and only
what arrives through that port is treated as coming from the proxy. With the proxy **on another
machine**, see [Proxy on another machine](#proxy-on-another-machine).

## 3. Start the server

```sh
sudo docker compose up -d --wait
```

The first time, it downloads the images (`--build` builds the server's from source). PostgreSQL
and the server come back by themselves when the machine reboots.

**Check.** Both services show as `healthy` in `sudo docker compose ps`, and:

```sh
sudo docker compose logs server | grep "no ar"
```

```
server-1  | {"time":"…","level":"INFO","msg":"invenire-server no ar, em HTTP atrás de proxy","version":"dev","listen":":8080","tls":"proxy","trusted_proxies":"172.31.250.0/24"}
```

And the port answers only on this machine:

```sh
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/healthz
```

It must show `204`.

## 4. Certificate

If Nginx already has a certificate for the domain, skip to step 5 and use its paths. Otherwise,
with the domain already pointing to this machine and port 80 open (Debian and Ubuntu):

```sh
sudo apt install nginx certbot python3-certbot-nginx
sudo certbot certonly --nginx -d invenire.exemplo.org
```

The certificate goes to `/etc/letsencrypt/live/invenire.exemplo.org/` and certbot renews it by
itself. For Nginx to start using the renewed one:

```sh
printf '#!/bin/sh\nsystemctl reload nginx\n' | sudo tee /etc/letsencrypt/renewal-hooks/deploy/reload-nginx
sudo chmod +x /etc/letsencrypt/renewal-hooks/deploy/reload-nginx
sudo certbot renew --dry-run
```

## 5. Nginx

```sh
sudo cp /opt/invenire/deploy/servidor/nginx/invenire.conf /etc/nginx/conf.d/invenire.conf
sudo sed -i 's/invenire.exemplo.org/YOUR.DOMAIN/g' /etc/nginx/conf.d/invenire.conf
sudo nginx -t && sudo systemctl reload nginx
```

(On Debian and Ubuntu, `conf.d/` is already read by `nginx.conf`. On other distributions, use the
folder `nginx.conf` includes, such as `http.d/` on Alpine.)

The whole file, with the reason for each line (comments translated here; the file in the
repository has them in Portuguese):

```nginx
server {
    listen 80;
    listen [::]:80;
    server_name invenire.exemplo.org;
    # Nothing from Invenire goes over HTTP: the enrollment token would travel in clear text.
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;    # Nginx 1.25.1+; before that: "listen 443 ssl http2;" and without this line
    server_name invenire.exemplo.org;

    ssl_certificate     /etc/letsencrypt/live/invenire.exemplo.org/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/invenire.exemplo.org/privkey.pem;
    # The agent requires TLS 1.3; 1.2 is there for older browsers.
    ssl_protocols TLSv1.2 TLSv1.3;

    # A gzipped snapshot can reach 32 MiB (the server's limit).
    client_max_body_size 32m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;

        proxy_set_header Host              $host;
        # $remote_addr, not $proxy_add_x_forwarded_for: discards whatever the
        # client may have sent in this header.
        proxy_set_header X-Forwarded-For   $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Host  $host;

        # Large snapshots over slow links.
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
    }
}
```

| Line | Without it |
|---|---|
| `return 301 https://…` in `listen 80` | the dashboard and enrollment would work over HTTP, with password and token in clear text; the server rejects with 403 whatever the proxy says arrived over HTTP |
| `ssl_protocols … TLSv1.3` | the agent does not connect (it only accepts TLS 1.3) |
| `client_max_body_size 32m` | Nginx's default is 1 MiB: machines with lots of software get `413`; the inventory stays in the agent's queue and does not get through until the limit is raised |
| `proxy_set_header Host $host` | the dashboard builds the installers' address from `Host`: without it, the packages would send machines to `127.0.0.1:8080` |
| `X-Forwarded-For $remote_addr` | logs and the login attempt limit would see everyone with the proxy's IP |
| `X-Forwarded-Proto $scheme` | no security problem, but the server loses confirmation that the request arrived over HTTPS |
| `proxy_read_timeout`/`proxy_send_timeout 300s` | a large snapshot over a slow link may be cut off at 60 s (the default) and resent later |

Nginx does **not** need `ssl_verify_client` or to forward a client certificate: the agent
authenticates with its signature. That way the browser of whoever opens the dashboard does not
show a certificate picker.

**Check**, from any machine:

```sh
curl -s -o /dev/null -w '%{http_code}\n' https://invenire.exemplo.org/healthz
curl -s -o /dev/null -w '%{http_code} %{redirect_url}\n' http://invenire.exemplo.org/
```

The first shows `204`, with no certificate error; the second, `301 https://invenire.exemplo.org/`.
A `502` means Nginx cannot reach the server (step 3); a `403` with "fora de --trusted-proxies"
(outside --trusted-proxies) means the proxy is connecting from an address the server does not
know (see [Proxy on another machine](#proxy-on-another-machine)).

## 6. First-time setup in the dashboard

Get the code:

```sh
sudo docker compose logs server | grep "primeira configuração"
```

Open `https://invenire.exemplo.org/` in the browser, with no certificate warning. It leads to
**Primeira configuração** (First-time setup): fill in the code, the organization name and the
first administrator and click **Criar organização** (Create organization).

**Check.** You land, logged in, on **Adicionar máquinas** (Add machines), with "Tudo pronto para
começar" (All set to start).

## 7. Enrollment token

In **Adicionar máquinas**, create a unit (if you want), and in **Novo token de cadastro** (New
enrollment token) choose the unit, **Quantas máquinas** (how many machines) and the **Validade**
(validity), and click **Criar token** (Create token).

**Check.** The **Endereço do servidor** (server address) is `https://invenire.exemplo.org` and
the **Pin da CA** (CA pin) says "nenhum: o servidor usa certificado público" (none: the server
uses a public certificate). If the address comes with `127.0.0.1` or port 8080, Nginx is not
forwarding `Host`.

## 8. Install on the first machine

With a public certificate there are two ways that need nothing beyond pasting:

**One-line command** (in **2. Comando de uma linha**, on the token screen). On Linux:

```sh
curl -fsSL https://invenire.exemplo.org/download/instalar.sh | sudo INVENIRE_ENROLL_TOKEN=inv_… sh
```

On Windows, the command on the screen, in PowerShell **as administrator**. The token goes in an
environment variable, never in the URL (which would end up in the proxy's log).

**Package to download**, as on the
[local network](local-network.md#6-install-on-the-first-machine): a `.zip` with `Instalar.cmd`
on Windows, a `.tar.gz` with `instalar.sh` on Linux.

**Check.** The installer ends with
`pronto: serviço invenire-agent ativo; a primeira coleta já foi enviada ou está na fila`.

## 9. See the machine `em-dia`

In the dashboard, in **Visão geral** (Overview), the machine shows up within a few seconds as
`em-dia` (up to date). From the command line, in the `deploy/servidor` folder:

```sh
sudo docker compose exec server invenire-server status --tenant "Minha Organização"
```

If it does not show up, see [troubleshooting.md](../troubleshooting.md#reverse-proxy).

## Next

- **Backup**: [operations.md](../operations.md#backup).
- **More machines**, agent updates and uninstalling: [agents.md](../agents.md).

## Proxy on another machine

When Nginx is on one machine and Invenire on another, the server's HTTP port needs to be
published on the local network IP, and the server needs to know where the proxy connects from.
In the `.env` of the Invenire machine (the proxy is `192.168.0.2`, Invenire `192.168.0.10`):

```ini
INVENIRE_BIND=192.168.0.10
INVENIRE_TRUSTED_PROXIES=192.168.0.2/32
```

In Nginx, `proxy_pass http://192.168.0.10:8080;`. Close port 8080 in the firewall to everything
except the proxy: traffic through it is plain HTTP.

`--trusted-proxies` (here, `INVENIRE_TRUSTED_PROXIES`) is mandatory in proxy mode: the
`X-Forwarded-*` headers are read only from connections coming from those ranges. From any other,
a request carrying `X-Forwarded-For`, `X-Forwarded-Proto` or `Forwarded` gets `403`, so no one
can forge their own address. With the proxy on the same machine, the default
(`172.31.250.0/24`, the compose network) already covers it: Nginx's connection to
`127.0.0.1:8080` reaches the container from that network's gateway.

## Other proxies

There are two ways to put a proxy in front of Invenire:

- **Terminating TLS** (the case above): the proxy has the public certificate, sees the content
  of the requests and forwards over HTTP to the server, which runs with `--behind-proxy`
  (`compose.proxy.yml` or `compose.caddy.yml`). The agent proves who it is with the signature on
  every request, which passes untouched through any HTTP proxy. This is the recommended way.
- **Forwarding TLS untouched** (passthrough): the proxy only looks at the requested name (SNI)
  and forwards the encrypted bytes to the server, which stays in direct mode (`compose.yml`, port
  8443, the installation's CA). The proxy sees nothing; agents enroll **with a pin**, and the
  browser warns about the certificate, as on the [local network](local-network.md). This is for
  sharing port 443 with other sites without handing TLS to the proxy. The public name must be in
  `INVENIRE_HOSTS`. The server then sees every connection as coming from the proxy's IP (logs and
  login attempt limit), and it does not understand the PROXY protocol: do not turn it on.

| Proxy | Terminating TLS (`--behind-proxy`) | Forwarding TLS untouched (passthrough) |
|---|---|---|
| **Nginx** | **yes, tested** (recipe above) | **yes, tested**, with `stream` + `ssl_preread` ([example](#nginx-in-passthrough)) |
| **Caddy** | **yes, tested** ([`compose.caddy.yml`](vps.md)) | only with the layer4 plugin; not tested |
| Nginx Proxy Manager | yes, a regular "Proxy Host"; not tested | through the "Streams" tab (without NPM's Let's Encrypt); not tested |
| Traefik | yes, via labels; not tested | yes, `HostSNI` + `tls.passthrough`; not tested |
| HAProxy | yes (`mode http`); not tested | yes (`mode tcp`); not tested |
| Apache (`mod_proxy`) | yes; not tested | no: Apache does not forward TLS without opening it |
| Cloudflare Tunnel / Cloudflare proxy | yes, thanks to the signature; not tested | no: Cloudflare always opens TLS |

"Not tested" means the configuration follows what the proxy documents and what the server
requires (below), but no one has run it end to end with Invenire yet. If you do, tell us how it
went.

Any proxy that terminates TLS works if it:

1. accepts **TLS 1.3** on the client side (the agent accepts nothing less);
2. accepts bodies of up to **32 MiB** and **does not touch the body** (the signature covers the
   SHA-256 of the body as the agent sent it, gzipped; a proxy that decompresses, recompresses or
   rewrites the body makes the server reject with `401` "assinatura inválida" (invalid
   signature));
3. forwards the original **`Host`** and sets **`X-Forwarded-For`** and
   **`X-Forwarded-Proto: https`**;
4. connects to the server from an address listed in `--trusted-proxies`;
5. publishes Invenire at the **root** of a domain, not on a subpath;
6. does **not** ask for a client certificate or require its own login in front (the agent would
   not know how to answer).

**What does not work**, and why:

- **An old agent behind a proxy that terminates TLS.** Agents from before signature
  authentication identify themselves only with the client certificate in TLS, which the proxy
  does not forward. They get `401`. Update the agent before putting the proxy in front, or use
  passthrough.
- **A proxy that cuts large bodies** (Nginx's default `client_max_body_size` is 1 MiB): `413`.
  The inventory stays in the agent's queue, which retries with increasing backoff; the machines
  with the most software stay `sem-coleta` (no collection) or `atrasado` (late) until the limit
  is raised. Since the queue goes from oldest to newest, the following inventories wait behind it
  (the queue holds up to 100). Only a `413` that the server itself marks as its own (body above
  32 MiB) is final and goes to `queue/rejected/`.
- **A proxy that rewrites the body** (some WAFs, "optimized" compression): `401` "assinatura
  inválida" (invalid signature).

None of these `401`s stop the agent. It only considers itself revoked when the server says it
rejected the badge itself (agent revoked or unknown, badge expired; the response carries a code
for that). Any other `401`, from the server or from a proxy page, is treated as a transient
failure: the log says `envio falhou; snapshot fica na fila` (send failed; snapshot stays queued)
with "não é revogação" (not a revocation), the inventories wait in the queue, and sending resumes
by itself once the proxy is fixed, without restarting the service or re-enrolling. A rejection
because of a wrong clock does not stop the agent either: it corrects the time from the server's
response and tries again.

Agents from before this rule take any `401` as revocation: the service stops (exit code 3) and
does not restart by itself. After fixing the proxy, restart the service on those machines
(`systemctl restart invenire-agent` or `Restart-Service invenire-agent`); no need to re-enroll.
They also send an inventory rejected with `413` to `queue/rejected/`.
- **Passthrough with a proxy that opens TLS** (Apache, Cloudflare): there is no passthrough; use
  the TLS-terminating mode.

### Nginx in passthrough

Tested with Nginx 1.26 (Alpine). The server runs with `compose.yml` (direct mode), and in `.env`:

```ini
COMPOSE_FILE=compose.yml
INVENIRE_HOSTS=invenire.exemplo.org
INVENIRE_PORT=127.0.0.1:8443
```

Port 8443 stays only on `127.0.0.1`: from outside, only through Nginx. In `nginx.conf`,
**outside** the `http` block (requires the `stream` module; on Debian and Ubuntu it comes in the
`libnginx-mod-stream` package). If your distribution already ships a `stream` block (Alpine does,
in `conf.d/stream.conf`, including `stream.d/*.conf`), put only the `map` and the `server` inside
it:

```nginx
stream {
    map $ssl_preread_server_name $invenire_destino {
        invenire.exemplo.org  127.0.0.1:8443;
        default               127.0.0.1:8444;   # the other sites, in an http server on 8444
    }
    server {
        listen 443;
        ssl_preread on;
        proxy_pass $invenire_destino;
    }
}
```

Nginx's other sites now listen on `127.0.0.1:8444` (with `ssl`), since 443 belongs to `stream`.
Agents enroll with the pin, at the address `https://invenire.exemplo.org`.
