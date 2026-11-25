[Português](../../instalar/escolha.md) | English

# Which scenario is yours

The Invenire server (`invenire-server` with PostgreSQL, in Docker) can run in three ways. All of
them use the repository's `deploy/servidor/` folder; only the compose file changes, chosen in
`.env` (`COMPOSE_FILE`). In all of them, first-time setup and day-to-day work happen in the
dashboard in the browser, and machines install the agent with a package downloaded from the
dashboard.

Answer from top to bottom and stop at the first row that matches:

| Question | If yes | Guide | File |
|---|---|---|---|
| Is there already a reverse proxy (Nginx, Traefik, Nginx Proxy Manager…) with a domain and a public certificate in front of what will run Invenire? | it terminates TLS and forwards to Invenire | [Reverse proxy](reverse-proxy.md) | `compose.proxy.yml` |
| Do you have a domain pointing to the machine, with its ports 80 and 443 open to the internet (a VPS, for example)? | Caddy obtains the certificate by itself | [VPS](vps.md) | `compose.caddy.yml` |
| Are all machines on the same network (or VPN) as the server, with no domain or public certificate? | the server uses the installation's own CA, and agents recognize it by the pin | [Local network](local-network.md) | `compose.yml` |

If you are unsure between the three, the simplest to start with is the **local network**: no
domain, certificate or port open to the internet.

## How they differ

| | Local network | VPS | Reverse proxy |
|---|---|---|---|
| Address the machines use | `https://192.168.0.10:8443` | `https://invenire.exemplo.org` | `https://invenire.exemplo.org` |
| Certificate | from the installation's CA | public (Let's Encrypt, via Caddy) | public (the proxy's) |
| Browser warns about the certificate | yes (expected) | no | no |
| How the agent checks the server | by the pin included in the package | by the system CAs | by the system CAs |
| How the server checks the agent | client certificate (mTLS) or signature | signature on every request | signature on every request |
| One-line install command | no (package only) | yes | yes |
| Machines outside the network | only via VPN | yes | yes |
| Open ports | 8443 on the internal network | 80 and 443 on the internet | the proxy's |

## Common questions

**I have a domain, but the machine is not exposed to the internet.** Without port 80 open, Let's
Encrypt cannot validate the domain through Caddy. Use the [local network](local-network.md), or,
if you already have a certificate for the domain, a [reverse proxy](reverse-proxy.md) with it.

**I already have an Nginx, but it must not see the content** (it forwards TLS untouched). That is
passthrough mode, explained in [reverse-proxy.md](reverse-proxy.md#other-proxies): the server
stays as on the local network, and the proxy only forwards.

**Can I switch scenarios later?** Yes: all three share the same data (the volumes of the
`invenire-servidor` compose project). Stop the current one (`docker compose down`, **without**
`-v`), change `COMPOSE_FILE` and start again. Agents already enrolled need to be pointed to the
new address, without re-enrolling ([agents.md](../agents.md#changing-the-server)).

**And without Docker?** The server is a single binary; see [operations.md](../operations.md#without-docker).
