[Português](../seguranca.md) | English

# Security

Who trusts whom, what each piece sees and the design choices behind it. To report a
vulnerability, contact the project maintainers before publishing it (see
[SECURITY.en.md](../../SECURITY.en.md)).

## Two separate trusts

| | Who checks | With what |
|---|---|---|
| The agent checks the server | the agent | the **installation CA** (`data/ca`), via the pin included in the installer package; or, with a public certificate (proxy, Caddy, `--tls-cert`), the system CAs, like a browser |
| The server checks the agent | the server | the agent's **organization CA** (tenant): one per organization, independent, created at the first enrollment |

The installation CA only exists in direct mode and issues, on every start, the server's TLS
certificate for the names in `INVENIRE_HOSTS` (`--hosts`). The pin is the SHA-256 of that CA's
certificate: it is not a secret, and it is the same for every machine.

Each organization's CA lives in the database (`tenant_ca`), with its key encrypted (AES-256-GCM)
by the **master key** (`data/master.key`, or `INVENIRE_MASTER_KEY`). Without the master key the
server can still check the badges already issued, but cannot issue new ones. Since the
organization's CA does not depend on the installation, moving an organization to another server
means taking its CA along, and agents only change address. Revoking an organization's CA
(`tenant_ca.revoked_at`) brings down all of its badges at once; the next enrollment creates a new
CA.

Agents enrolled before per-organization CAs have a badge from the installation CA. At startup, the
server registers that CA, for checking only, in each organization that has such agents.

## The agent and the server

**Enrollment.** The enrollment token (`inv_…`) is a limited-use password: it authorizes one
machine (or `N`, in a batch) to enroll until it expires. The database keeps only its SHA-256, and
it is shown once. Revoking a token does not affect machines already enrolled. Enrollment sends a
CSR and receives the **badge** (client certificate) signed by the organization's CA. Each machine
has one agent: enrolling the same machine again revokes the previous one. The machine identifier
is declared by whoever enrolls; if someone uses a token to impersonate another machine, that
machine's agent starts getting `401` instead of the two coexisting.

**Every request.** The agent identifies itself in two ways at once:

- **mTLS**: the badge goes in TLS. It only works when TLS terminates at the server itself (direct
  mode);
- **signature**: the headers `Invenire-Agent-Cert` (the badge), `Invenire-Timestamp` and
  `Invenire-Signature`, a signature with the badge key over the method, the API path, the
  timestamp and the SHA-256 of the body **as sent** (gzipped). It passes untouched through any
  HTTP proxy.

The server checks the signature and then the badge exactly as with mTLS: chain up to the
organization's CA and revocation. In direct mode both are valid, and mTLS takes precedence when
the client certificate comes in TLS; behind a proxy (`--behind-proxy`), only the signature. The
proxy does not have the badge key: it cannot impersonate an agent.

**Clock.** `Invenire-Timestamp` may be up to **24 hours** away from the server's clock, in either
direction: wrong clocks are common in a fleet. If the machine is further off than that, the agent
corrects the timestamp from the response's `Date` and tries again once.

**Replay.** There is no nonce. Replaying a captured `POST /v1/snapshots` gives `200` "repetido"
(repeated) and changes nothing; neither does a `GET /v1/agent`. Replaying a
`POST /v1/agent/renew` would bring down the badge the agent is already using, so the server keeps
the timestamp of the last signed renewal (`agent.last_renew_signed_at`, migration `00011`) and
only accepts another with a later timestamp.

**Size.** The limit (32 MiB per snapshot, as it arrives) is checked before reading the body.

## What the proxy sees

- **Terminating TLS** (`compose.proxy.yml`, `compose.caddy.yml`): the proxy sees everything that
  passes through it in clear text: dashboard passwords and sessions, enrollment tokens, API tokens
  and the inventories. It **cannot** impersonate an enrolled agent (it does not have the badge
  key), but, since it sees enrollment tokens, it could enroll a fake machine with a token that is
  still valid. Use a proxy you control.
- **Passthrough**: the proxy sees only the requested name (SNI) and the encrypted bytes.

**`--trusted-proxies`.** Behind a proxy, the server only reads `X-Forwarded-For` and
`X-Forwarded-Proto` from connections coming from the listed ranges. From any other, a request with
`X-Forwarded-*` or `Forwarded` gets `403`, so no one can forge their own address. The client
address is the rightmost one in `X-Forwarded-For` that is not a trusted proxy. An
`X-Forwarded-Proto` other than `https` also gets `403`: a password or token would have travelled
in clear text. In `compose.proxy.yml` the default range is the compose network, because a proxy on
the same machine arrives through it; this means **any process on the machine** that can reach port
`127.0.0.1:8080` is treated as the proxy.

## Dashboard

- **First-time setup.** On an installation without an organization, `/setup` requires the
  **setup code**, shown once in the server log (or given in `INVENIRE_SETUP_CODE`): whoever first
  finds a new installation exposed to the internet cannot take it over. The code has about 60
  bits, is valid until the first organization is created, and each start without an organization
  generates another. Do the setup right after starting.
- **Login.** Email (unique in the installation) and password stored with **argon2id**. Session in
  the `__Host-invenire-sessao` cookie (`HttpOnly`, `Secure`, `SameSite=Lax`); the database keeps
  only the hash of the identifier. The session ends after 12 hours of inactivity and after 7 days
  in any case. After 10 wrong passwords (per email and per address), login is locked for 15
  minutes.
- **Forms.** A CSRF token on every logged-in screen, and rejection of submissions from another
  origin (`Sec-Fetch-Site`/`Origin`), on login and setup too.
- **Permissions.** The actor's scopes apply in the dashboard as in the API: `read` only views;
  `admin` can also change things. The organization always comes from the session, never from the
  URL or the form. Every action goes through the same functions as the `/v1` routes and is recorded
  in `change_log` with who did it.
- **Headers.** `Content-Security-Policy` for the page's own origin only, no framing
  (`frame-ancestors 'none'`), `nosniff`, and pages kept out of the cache.
- **Access recovery** is through the server's command line (`user set-password`): whoever has
  access to the server recovers the account. No email is sent.

The dashboard does not separate "organization administrator" from "installation operator": whoever
looks after the installation is whoever has the server's command line.

## Installer package

The package downloaded from the dashboard **contains the enrollment token** (in `instalar.conf`).
The server does not store the token to build it: the download button is a `POST` form that sends
back to the server the token the page is showing, with the administrator session and CSRF; the
server checks by hash that the token belongs to the session's organization and is still valid,
builds the package in memory and delivers it. Each downloaded package is recorded in `change_log`
(`enrollment_package_downloaded`).

Treat the package like a password: short validity, few uses, revoke it if it leaks. After
installing, the installer deletes `instalar.conf` if the token was for a single machine.

The agent executables are public (`/download/`): they are not secret. The installers check them
against `SHA256SUMS` before running. That `SHA256SUMS` comes from the same server, so the trust is
in the channel: the pin (inside the package) or the public certificate. That is why, with a pin,
there is no one-line command: the download would have no way to check the server. The executables
do not have code signing yet.

## Text coming from the machines

Whoever controls an inventoried machine controls the text it sends: hostname, software names,
vendor, version, path. The server stores it without control or invisible formatting characters
and truncated; the dashboard always escapes it (it never becomes HTML); the API delivers it inside
`collected`. It is data, not instructions, including for AI agents reading the API. A snapshot
with `\u0000` is rejected with `422`.

## Stored data

Each snapshot is stored twice: `payload` (jsonb, for queries) and `body` (the exact bytes the
agent sent, compressed with lz4). A `CHECK` in the database guarantees `sha256(body) = sha256`,
and each snapshot points to the previous one (`prev_sha256`): a machine's chain can be re-verified
using only what is in the database, and the export carries the original bytes.
