[Português](../operacao.md) | English

# Server operations

Day-to-day work for whoever looks after a server installed with one of the
[guides](install/choosing.md): backup, restore, updates, monitoring and the administration
command line. Commands run in the `deploy/servidor` folder, where `.env` says which compose file
is in use (`COMPOSE_FILE`); that way none of them needs `-f`.

## Administration command line

Almost everything is done in the dashboard. What is not (or what you want in scripts) runs inside
the container:

```sh
cd /opt/invenire/deploy/servidor
sudo docker compose exec server invenire-server help
```

To avoid repeating the prefix, a shell function (put it in `~/.bashrc` to keep it):

```sh
inv() { (cd /opt/invenire/deploy/servidor && sudo docker compose exec server invenire-server "$@"); }
inv status --tenant "Minha Organização"
```

| Command | What for |
|---|---|
| `status --tenant <org> [--stale 12h] [--check]` | the fleet, one machine per line, those needing attention first |
| `user set-password --email <email>` | changes a dashboard login password (for whoever forgot it) and ends their sessions; the password comes from `INVENIRE_PASSWORD` or is generated and shown |
| `tenant create [--admin-email <email> --admin-name <name>] <org>` | creates another organization (the first one is created by `/setup`) |
| `token create --tenant <org> [--unit <path>] [--uses N] [--ttl 24h] [--description …]` | enrollment token, as in **Adicionar máquinas** (Add machines) |
| `token list --tenant <org> [--all]` / `token revoke --tenant <org> <id>` | enrollment tokens and how many uses are left; revoke one that leaked |
| `unit create --tenant <org> [--parent <path>] [--kind <type>] <name>` / `unit list --tenant <org>` | units |
| `actor create --tenant <org> --name <name> --scopes read,admin [--kind human\|ai_agent\|ci] [--ttl …]` | user or pipeline with an API token (`inva_…`) |
| `tenant export --tenant <org> --out <file> [--with-ca-key]` / `tenant verify <file>` | organization export ([export.md](export.md)) |
| `health [--url …]` | checks the local `/healthz` (it is the image's healthcheck) |
| `version` | server version |

## Backup

```sh
cd /opt/invenire/deploy/servidor
sudo ./backup.sh /var/backups/invenire
```

It writes, into a folder named with the date and time, the database dump (`invenire.dump`) and
the server data (`data/`: the installation's CA and the master key), with a `SHA256SUMS`, and
keeps the 14 most recent. **One without the other does not restore:** without the master key the
server does not issue new badges; without the installation's CA, agents with a pin stop
recognizing the server.

For a daily run, in root's cron (`sudo crontab -e`):

```
0 2 * * * cd /opt/invenire/deploy/servidor && ./backup.sh /var/backups/invenire
```

Copy the backups folder **off** the machine (another server, external disk, cloud storage). Also
keep the database password (`INVENIRE_PG_PASSWORD`, in `.env`). Caddy's certificate (VPS
scenario) is not part of the backup and does not need to be: on a new machine, Caddy obtains
another one.

**Check:** the script ends with `backup em /var/backups/invenire/<date>`, and the folder has
`invenire.dump`, `data/master.key`, `data/ca/` and `SHA256SUMS`.

## Restore

On a new machine (or with empty volumes), with the repository in `/opt/invenire` and `.env`
filled in (the same `COMPOSE_FILE`; the database password may be different, the dump does not
carry it):

```sh
cd /opt/invenire/deploy/servidor
sudo ./restaurar.sh /var/backups/invenire/20261110-161950
```

The script checks the backup against `SHA256SUMS`, restores the database, puts the server data in
the volume with the right owner and starts everything. It **erases** whatever is in the database
and in the server data.

Restored with the same CA and at the same address, agents carry on with nothing changed on them.
If the address changed, point each agent to the new one
([agents.md](agents.md#changing-the-server)).

**Check:** `sudo docker compose exec server invenire-server status --tenant "Minha
Organização"` shows the machines; at each one's next collection (or after restarting the agent
service) it goes back to `em-dia` (up to date).

Tested: backup, `docker compose down -v` (deletes the volumes), restore, and the agent going back
to `em-dia` with no change at all on it.

A portable copy of an organization, to keep or to take to another server, is `tenant export`
([export.md](export.md)).

## Updating the server

```sh
cd /opt/invenire
sudo git pull
cd deploy/servidor
sudo ./backup.sh /var/backups/invenire
sudo docker compose pull
sudo docker compose up -d --wait
```

With `INVENIRE_VERSION` pinned in `.env`, change it to the new version before `pull` (versions
are listed in the [CHANGELOG](../../CHANGELOG.md), in Portuguese, and in the repository's
releases). If you build the image from source, replace `pull` with `up -d --build --wait`.

The server applies database migrations at startup. The new image also includes the new agent:
machines get it when the installer runs again ([agents.md](agents.md#updating)).

## Monitoring

- **Dashboard:** **Visão geral** (Overview) shows the machines needing attention, with what to do
  in each state ([troubleshooting.md](troubleshooting.md#machine-states)).
- **Cron:** `status --check` exits with code 1 if any machine needs attention:

  ```
  0 8 * * * cd /opt/invenire/deploy/servidor && docker compose exec -T server invenire-server status --tenant "Minha Organização" --check > /dev/null || <notify>
  ```

- **Service health:** `GET /healthz` answers `204` when the server and the database respond
  (through the public address, or `docker compose ps`, which shows the healthcheck).
- **Logs:** `sudo docker compose logs server` (JSON, one line per event). Ingestion errors also
  show at the end of `status` and in **Visão geral** > **Erros recentes** (Recent errors).

## Data and keys

On first start the server creates, in the `data` volume (`/var/lib/invenire/data` in the
container):

- `ca/`: the installation's CA, which issues the server's TLS certificate in direct mode
  (`compose.yml`), for the names in `INVENIRE_HOSTS`. Behind a proxy it is not created;
- `master.key`: the master key, which encrypts (AES-256-GCM) each organization's CA key, stored in
  the database. It can come from `INVENIRE_MASTER_KEY` (32 bytes in base64) instead of the file.

The first-time setup code and the login attempt limit live in the process's memory: with more
than one server replica, each would have its own. Run just one.

## Without Docker

The server is a single binary (`invenire-server`), which needs PostgreSQL 17 and its URL in
`INVENIRE_DATABASE_URL`. Built as in [development.md](development.md), it starts with:

```sh
export INVENIRE_DATABASE_URL="postgres://invenire:SENHA@localhost:5432/invenire?sslmode=disable"
invenire-server serve --listen :8443 --data /var/lib/invenire/data --hosts invenire.local,192.168.0.10 --agent-dir /opt/invenire/dist/0.1.0
```

`serve` options:

| Option | Default | Does |
|---|---|---|
| `--listen` | `:8443` | listen address |
| `--data` | `data` | folder for the installation's CA and the master key |
| `--hosts` | `localhost,127.0.0.1` | names and IPs in the server certificate (direct mode) |
| `--tls-cert`, `--tls-key` | | public certificate (PEM with the chain) instead of the installation CA's; reloaded by itself when the file changes (certbot renewal) |
| `--behind-proxy` | | plain HTTP behind a proxy that terminates TLS ([reverse-proxy.md](install/reverse-proxy.md)) |
| `--trusted-proxies` | | with `--behind-proxy`, mandatory: IPs or CIDRs the proxy connects from |
| `--agent-dir` | `INVENIRE_AGENT_DIR`, or the image's folder | agent executables and `SHA256SUMS` for `/download/` and the installer package (the `scripts/build.sh` folder works) |
| `--stale` | `12h` | in the dashboard, no news from the agent for longer than this = `atrasado` (late) |
| `--auto-map-confidence` | `0.9` | minimum confidence for a rule to map software without review |

Without the agent executables, the server starts anyway, warns in the log, and the dashboard
explains that it cannot build the package.
