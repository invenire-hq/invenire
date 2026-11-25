[Português](../problemas.md) | English

# When things go wrong

Look for the message (or the symptom) in the table for your stage. Messages are shown in
Portuguese, as the software prints them, with a translation in parentheses where it helps. If it
is not here, open an issue with the full message, the machine's system and the version of the
agent (`invenire-agent version`) and of the server (`invenire-server version`).

## Machine states

What shows in **Visão geral** (Overview) and in `invenire-server status`, those needing attention
first:

| State | Means | What to do |
|---|---|---|
| `em-dia` (up to date) | the agent checked in within the limit and everything was ingested | nothing |
| `na-fila` (queued) | inventories waiting for ingestion | normal for a few seconds; if it persists, check the server logs |
| `sem-coleta` (no collection) | enrolled, but no inventory has arrived | the service did not start or cannot reach the server: check the agent log ([agents.md](agents.md#checking)) |
| `atrasado` (late) | the agent has not checked in for more than 12h (`--stale`) | machine switched off, no network, or service stopped |
| `erro` (error) | some inventory could not be ingested | the reason shows at the end of `status` and in **Erros recentes** (Recent errors); open an issue with it |
| `sem-agente` (no agent) | the agent was revoked (or the machine was enrolled again) | re-enroll if the machine is still yours |

"Corrente quebrada" (broken chain) next to a machine means an inventory did not continue the
previous one: clock changed, image restore, or two cloned machines with the same identifier (they
take turns on the same enrollment, and each enrollment revokes the previous one).

## Installing the agent

| Message | Cause | What to do |
|---|---|---|
| `certificate is valid for X, not Y` | the server (direct mode) started without the name Y in `INVENIRE_HOSTS` | add Y to `INVENIRE_HOSTS` in `.env`, `docker compose up -d`, and run the installer again |
| `pin da CA inválido` (invalid CA pin) | the value of `--ca-pin` is not the pin (maybe the token in its place) | use the dashboard package, or copy the whole pin, with `sha256:` |
| `a CA do servidor não confere com o pin` (the server's CA does not match the pin) | the pin is from another server, or the server data was lost and the CA is different | generate the package again in the right server's dashboard |
| `servidor respondeu 401: token de cadastro inválido` (server answered 401: invalid enrollment token) | token already used up, expired, revoked or incomplete | create another token in the dashboard |
| `rode como administrador` / `precisa rodar como root` (run as administrator / must run as root) | terminal without elevation | Windows: double-click `Instalar.cmd` and **Yes** at the UAC prompt; Linux: `sudo` |
| `o agente não está cadastrado` (the agent is not enrolled) | `service install` before `enroll` | use the installer, or run `enroll` first |
| `este sistema não usa systemd` (this system does not use systemd) | Linux without systemd | the installer only creates systemd services; run `invenire-agent run` under another supervisor |
| `o programa do agente não está na pasta` (the agent program is not in the folder) (with a pin) | release-folder script without the executable, server with a pin | use the dashboard package, which includes the executable |
| `está diferente do que o servidor publicou` (differs from what the server published) | executable corrupted or modified | download the package again |
| "Windows protected your PC" | SmartScreen: the executable has no code signing | **More info** > **Run anyway** ([agents.md](agents.md#windows-smartscreen-and-antivirus)) |
| `x509: certificate signed by unknown authority` (without a pin) | the server has no public certificate, or the machine does not trust its CA | with a certificate from the installation's CA, use the dashboard package (it carries the pin) |

## After installation

| Message (agent log) | Cause | What to do |
|---|---|---|
| `collected_at … à frente do servidor` (ahead of the server) (in `queue/rejected/`) | the machine's clock is more than 24h fast | fix the clock; the next collection gets in |
| `o servidor recusou o certificado do agente` (the server rejected the agent's certificate) (service stopped, exit 3) | agent revoked, machine enrolled again (each enrollment revokes the previous one) or badge expired | enroll again with the installer and `-Force`/`--force` |
| `envio falhou; snapshot fica na fila` (send failed; snapshot stays queued) with `401` and "não é revogação" (not a revocation) | a proxy along the way rejected or altered the request; the badge is still valid | fix the proxy (table below); the agent retries by itself |
| `envio falhou; snapshot fica na fila` with `413` | body limit of a proxy along the way | raise the limit (table below); the snapshot waits in the queue |
| `envio falhou; snapshot fica na fila` | server down or unreachable | nothing to do if it is transient; the agent retries and the queue holds up to 100 |
| `servidor recusou o snapshot` (server rejected the snapshot) (in `queue/rejected/`) | final rejection by the server (4xx), with the reason in the file | see the reason |

## Reverse proxy

| Symptom | Cause | What to do |
|---|---|---|
| `502 Bad Gateway` | the proxy cannot reach the server | `docker compose ps`; with the proxy on the same machine, `curl http://127.0.0.1:8080/healthz` must give `204` |
| `403` "… fora de --trusted-proxies" (outside --trusted-proxies) | the proxy connects from an address the server does not know | put the proxy's IP in `INVENIRE_TRUSTED_PROXIES` ([proxy on another machine](install/reverse-proxy.md#proxy-on-another-machine)) |
| `403` "o proxy recebeu esta requisição por http" (the proxy received this request over http) | the proxy forwards what arrived over HTTP | redirect HTTP to HTTPS at the proxy (`return 301`) |
| `413` (proxy page, "Request Entity Too Large") | proxy body limit below 32 MiB | `client_max_body_size 32m;` (Nginx); the snapshots wait in the queue and go out later |
| `401` "assinatura inválida" (invalid signature) | the proxy altered the body or the path | turn off whatever rewrites the body; publish at the root of the domain |
| `401` "crachá do agente obrigatório" (agent badge required) | old agent (mTLS only) behind a proxy that terminates TLS | update the agent, or use passthrough |
| `401` "Invenire-Timestamp fora da janela" (Invenire-Timestamp outside the window) | the machine's clock is more than 24h away from the server's (the agent already tried to correct it) | fix the clock on the machine and on the server |
| agent: `tls: protocol version not supported` | the proxy does not offer TLS 1.3 | `ssl_protocols TLSv1.2 TLSv1.3;` |
| package or command with address `127.0.0.1:8080` or `http://` | the proxy does not forward `Host` | `proxy_set_header Host $host;` |

## Dashboard

| Symptom | Cause | What to do |
|---|---|---|
| "connection not secure" warning | direct mode: the browser does not know the installation's CA | expected on the local network (**Advanced** > **Continue**); to avoid the warning, use a [proxy](install/reverse-proxy.md) or a [VPS](install/vps.md) |
| window asking you to choose a certificate | direct mode: the server asks for a client certificate because of the agents, and the browser has an e-CPF/e-CNPJ | **Cancel** |
| "Código de configuração errado." (Wrong setup code.) | code from another start (each start without an organization generates another) | take the one from the most recent line: `docker compose logs server \| grep "primeira configuração"` |
| `/setup` leads to the login | the organization has already been created | log in with the first administrator's email and password |
| "E-mail ou senha não conferem." (Email or password do not match.) / forgot the password | | `invenire-server user set-password --email <email>` ([operations.md](operations.md#administration-command-line)) |
| "Muitas tentativas." (Too many attempts.) | 10 wrong passwords (per email and per address) | wait 15 minutes, or change the password from the command line |
| "Seu acesso é só de consulta" (Your access is read-only) | `read` user | ask an administrator |
| "O formulário expirou ou não veio desta página." (The form expired or did not come from this page.) | new session since the page was opened | reload the page and try again |
| "O formulário não veio em UTF-8." (The form was not sent in UTF-8.) | form submitted by a script in another encoding | submit in UTF-8 |
| "Este servidor não tem os executáveis do agente" (This server does not have the agent executables) | server outside Docker without `--agent-dir` | point `--agent-dir` (or `INVENIRE_AGENT_DIR`) to the `scripts/build.sh` folder |
| the token page is gone after reloading | the token is shown only once | create another token |
