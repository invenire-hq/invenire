[Português](../painel.md) | English

# Web dashboard

`invenire-server` serves a dashboard in the browser, on the same port as the API: the API is at
`/v1`, the dashboard at `/`. There is nothing separate to install: HTML, styles and the little
JavaScript there is are embedded in the binary. Texts are in Portuguese, in a message catalog
(`internal/server/web/messages.go`) that allows another language later.

## First-time setup

On an installation with no organization, the server writes a `primeira configuração` (first-time
setup) line with a code to the log (or uses the one in `INVENIRE_SETUP_CODE`):

```sh
docker compose logs server | grep "primeira configuração"
```

Opening the server's address leads to `/setup`, which asks for that code, the organization name
and the first administrator (name, email and password). Without the right code, or with an
organization already created, `/setup` refuses. The dashboard then takes you to **Adicionar
máquinas** (Add machines), which offers to create the first unit and the first enrollment token.
The step-by-step for each scenario is in the [installation guides](install/choosing.md).

The same organization with an administrator can also be created from the command line:

```sh
invenire-server tenant create --admin-email ana@exemplo.org --admin-name "Ana" "Minha Organização"
```

(the password comes from `INVENIRE_PASSWORD`, or is generated and shown).

## Screens

- **Visão geral** (Overview): the same as `invenire-server status`, with what to do in each state
  ([troubleshooting.md](troubleshooting.md#machine-states)). Refreshes itself every minute.
- **Adicionar máquinas** (Add machines): enrollment tokens (create, list, revoke; the token is
  shown once) and the three ways to install the agent ([agents.md](agents.md#installing)).
- **Máquinas** (Machines): filters (unit, system, name), installed software, history, unit,
  labels, revoking the agent.
- **Unidades** (Units): tree, create, rename, move, delete.
- **Software**: installed products and where; unidentified ones, with the rules' suggestion and
  manual mapping.
- **Acesso** (Access): dashboard users (`read` or `admin`) and API tokens.
- **Exportar** (Export) the organization ([export.md](export.md)).
- **Minha conta** (My account): change your password. Whoever has more than one organization
  switches in **Organização** (Organization).

Every action goes through the same functions as the `/v1` routes and is recorded in `change_log`
with who did it. Login, sessions, permissions and the setup code are covered in
[security.md](security.md#dashboard).

## Forgot the password

Whoever has access to the server changes it:

```sh
docker compose exec server invenire-server user set-password --email ana@exemplo.org
```

The password comes from `INVENIRE_PASSWORD`, or is generated and shown; that person's open
sessions are ended.

## Direct mode and the browser

In direct mode (local network), the browser does not know the installation's CA and warns that
the certificate is not trusted; and, since the server asks for a client certificate in TLS
(because of the agents' mTLS), browsers with a digital certificate installed (such as the
Brazilian e-CPF or e-CNPJ) may show a picker: cancel it. Behind a proxy or with Caddy none of this
happens; the proxy only needs to forward `Host`, because the dashboard uses it to build the
address that goes into the installer packages.
