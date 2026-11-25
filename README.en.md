[Português](README.md) | English

# <img src="docs/marca/logo.svg" alt="" width="40" height="40" align="top"> Invenire

<img src="docs/marca/mascote.svg" alt="The Invenire owl, with a magnifying glass over one eye" width="120" align="right">

Machine inventory: what exists, where it is and what it is made of. A lightweight agent on each
machine (Windows and Linux) collects hardware, operating system and installed software and sends
them to a server, which keeps the history, identifies the products and shows everything in a web
dashboard and an API.

- **Server** (`invenire-server`): PostgreSQL, API at `/v1` and web dashboard on the same port, in
  Docker. First-time setup and day-to-day work in the browser.
- **Agent** (`invenire-agent`): a Windows service or a systemd service. Installs with two clicks
  from a package downloaded from the dashboard, or with a one-line command.
- **Three ways to run the server**: local network (no domain), VPS (Caddy with Let's Encrypt) and
  behind an existing reverse proxy (Nginx and others).
- Several organizations per server, units in a tree, labels, history of installs and removals,
  full organization export with the original bytes of every inventory.

The software itself (dashboard, messages, installer) is in Brazilian Portuguese; this
documentation keeps those on-screen texts as they appear, with a translation where it helps.

## Quick start (local network)

On a Linux machine with Docker and the compose plugin, which the other machines can reach on
port 8443:

```sh
sudo git clone https://github.com/invenire-hq/invenire.git /opt/invenire
cd /opt/invenire/deploy/servidor
sudo cp .env.example .env
sudo nano .env      # INVENIRE_PG_PASSWORD (openssl rand -hex 24) and INVENIRE_HOSTS (this machine's IP)
sudo docker compose up -d --wait
sudo docker compose logs server | grep "primeira configuração"
```

Open `https://<this machine's IP>:8443/` in the browser, accept the certificate warning (it comes
from the installation's own CA), enter the code from the log, and create the organization and the
first administrator. In **Adicionar máquinas** (Add machines), create a token and download the
installer package; on the machine, extract it and run `Instalar.cmd` (Windows) or
`sudo ./invenire-agente/instalar.sh` (Linux). Within a few seconds it shows up as `em-dia`
(up to date) in **Visão geral** (Overview).

The complete walkthrough, with how to check each step: [docs/en/install/local-network.md](docs/en/install/local-network.md).

## Documentation

- [Which scenario is yours](docs/en/install/choosing.md): [local network](docs/en/install/local-network.md),
  [VPS](docs/en/install/vps.md) or [reverse proxy](docs/en/install/reverse-proxy.md)
- [Agents](docs/en/agents.md), [dashboard](docs/en/dashboard.md), [operations](docs/en/operations.md)
  (backup, restore, updates) and [when things go wrong](docs/en/troubleshooting.md)
- [Security](docs/en/security.md), [API](docs/en/api.md),
  [how it works inside](docs/en/internals.md) and [export](docs/en/export.md)
- [Development](docs/en/development.md): build, tests, sqlc
- [Index of all documentation](docs/en/README.md)

`pkg/sysinfo` (reading host, OS and software) does not depend on the snapshot format, so it can be
shared with [PeritiaGo](https://github.com/athosbes/PeritiaGo).

## Contributing

Bug reports, ideas and code are welcome: see [CONTRIBUTING.en.md](CONTRIBUTING.en.md)
(environment, tests, commit conventions and the [contributor agreement](CLA.md), in Portuguese).
Security issues are reported privately, as described in [SECURITY.en.md](SECURITY.en.md). The
changes in each version are in [CHANGELOG.md](CHANGELOG.md) (in Portuguese).

## License

Invenire is free software, under the [GNU Affero General Public License, version 3](LICENSE)
(`AGPL-3.0-only`). You may use, study, modify and redistribute it; whoever distributes Invenire,
or offers a modified version as a network service, must offer the corresponding source code under
the same license. To use it under other terms (commercial license), contact the maintainer on
GitHub.
