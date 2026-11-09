# Invenire

Camada de inventário e contexto: o que existe, onde está e de que é feito.
A arquitetura e o modelo de dados estão no documento
[Invenire — Arquitetura e Modelo de Dados](https://claude.ai/artifact/WfzRB6TWaCNPQxdpx9QJtc).

## Estado (v0)

- [x] `pkg/snapshot` v1: structs Go, hash SHA-256 e JSON Schema gerado (`schema/v1.json`)
- [x] `pkg/sysinfo`: leitura de baixo nível de host, SO e software — Windows (SMBIOS via `GetSystemFirmwareTable`, registro) e Linux (DMI, os-release, dpkg, rpm). Só depende de `golang.org/x/sys` e não conhece o formato do snapshot, para poder virar o módulo compartilhado com o [PeritiaGo](https://github.com/athosbes/PeritiaGo)
- [x] `invenire-agent collect --out` offline
- [x] Migrações v0 (goose), incluindo `actor`, `proposal` e `change_log`, e consultas sqlc em `internal/server/store`
- [x] `invenire-server`: `POST /v1/enroll` (token de uso único, CSR, CA interna) e `POST /v1/snapshots` (mTLS, gzip, SHA-256, JSON Schema, encadeamento por `prev_sha256`)
- [x] `invenire-agent enroll` (CA conferida por pin) e `run` (coleta periódica, fila local em disco, envio com mTLS e gzip, espera exponencial)
- [x] Bytes originais de cada snapshot guardados em `snapshot.body`, com o hash conferido pelo próprio banco
- [x] Ingestão com diff de instalações (`internal/server/ingest`)
- [x] Regras de normalização (`internal/server/normalize`), lista de software não identificado e mapeamento manual pela API
- [x] `invenire-agent service install | uninstall`: serviço do Windows (SCM, Event Log) e unit do systemd
- [x] CA independente por tenant (crachás dos agentes), chave cifrada com a chave-mestra; servidor conferido por pin ou certificado público; `invenire-agent set-server`

## Do zero à primeira máquina

São três programas: o **PostgreSQL**, o **servidor** (`invenire-server`), que recebe e organiza os
inventários, e o **agente** (`invenire-agent`), instalado em cada máquina. Os comandos abaixo são
do PowerShell, na pasta do repositório; no Linux troque `bin\` por `bin/` e `$env:X="…"` por
`export X="…"`.

**1. Banco e binários** (uma vez)

```powershell
docker compose -f deploy/docker-compose.yml up -d --wait
go build -o bin/ ./cmd/invenire-server ./cmd/invenire-agent
```

**2. Servidor** (deixe este terminal aberto)

```powershell
$env:INVENIRE_DATABASE_URL="postgres://invenire:invenire@localhost:5442/invenire?sslmode=disable"
bin\invenire-server.exe serve --listen :8443 --data data --hosts localhost,127.0.0.1
```

`--hosts` lista os nomes e IPs pelos quais os agentes vão chamar o servidor. O endereço usado no
`enroll` (passo 4) precisa estar nessa lista, senão o agente recusa a conexão com
`certificate is valid for …, not …`. Para máquinas da rede, inclua o nome DNS e o IP do servidor:
`--hosts localhost,invenire.prefeitura.local,10.0.0.5`. A lista vale a partir da próxima subida.

Na primeira subida o servidor cria `data/ca/` e `data/master.key`. Guarde os dois com o backup
do banco (veja [Servidor](#servidor)).

**3. Organização e token de cadastro** (outro terminal, com a mesma `INVENIRE_DATABASE_URL`)

```powershell
bin\invenire-server.exe tenant create "Prefeitura"
bin\invenire-server.exe token create --tenant "Prefeitura" --ttl 24h --description "lote 1"
```

O `token create` mostra duas coisas diferentes, e o `enroll` pede as duas:

| | Parece com | Para que serve |
|---|---|---|
| **token** | `inv_9LYq…` | senha de uso único: autoriza **uma** máquina a se cadastrar. Depois de usado, ou vencido o `--ttl`, não vale mais |
| **ca pin** | `sha256:a6c6…` (64 dígitos hex) | impressão digital do servidor: o agente só fala com o servidor cuja CA tem esse SHA-256. É o mesmo para todas as máquinas e não é segredo |

Um token por máquina: para cadastrar dez, rode `token create` dez vezes.

**4. Cadastrar a máquina** (PowerShell **como administrador**; no Linux, root)

```powershell
bin\invenire-agent.exe enroll --server https://localhost:8443 --token inv_COLE_O_TOKEN_INTEIRO --ca-pin sha256:COLE_O_PIN
```

Para o token não ficar no histórico do terminal, ele pode ir em `INVENIRE_ENROLL_TOKEN` em vez
de `--token`.

**5. Deixar rodando como serviço** (mesmo terminal de administrador)

```powershell
bin\invenire-agent.exe service install
Get-Service invenire-agent
```

O serviço coleta na hora e depois a cada 4h (`--interval`). Para ver o que chegou, crie um
usuário da API (passo 6) ou consulte o banco.

**6. Usuário da API** (opcional)

```powershell
bin\invenire-server.exe actor create --tenant "Prefeitura" --name "athos" --scopes read,admin
curl.exe -k -H "Authorization: Bearer inva_COLE_O_TOKEN" https://localhost:8443/v1/software/unmapped
```

### Quando dá errado

| Mensagem | Causa | O que fazer |
|---|---|---|
| `certificate is valid for X, not Y` | o servidor subiu sem o nome Y em `--hosts` | suba o servidor de novo com Y em `--hosts` |
| `pin da CA inválido` | o valor de `--ca-pin` não é o pin (talvez o token no lugar dele) | copie a linha `ca pin:` inteira, com `sha256:` |
| `a CA do servidor não confere com o pin` | o pin é de outro servidor ou de outra pasta `data` | gere o token no mesmo servidor e `--data` |
| `servidor respondeu 401: token de cadastro inválido` | token já gasto, vencido ou incompleto | `token create` de novo |
| `rode como administrador` | terminal sem elevação | abra o PowerShell como administrador |
| `o agente não está cadastrado` | `service install` antes do `enroll` | faça o passo 4 |

## Agente

Coleta sem servidor, para conferir o que o agente vê ou para redes isoladas:

```sh
bin/invenire-agent collect --out snapshot.json
```

No Linux, rode como root: `product_uuid` e `product_serial` só são legíveis por ele.

### Cadastro e envio

```sh
invenire-agent enroll --server https://invenire.local:8443 --token inv_… --ca-pin sha256:…
invenire-agent run               # coleta a cada 4h (--interval) e envia
invenire-agent run --once        # uma coleta e uma tentativa de envio
```

O token e o pin vêm de `invenire-server token create`. O token também pode ir em
`INVENIRE_ENROLL_TOKEN`, para não aparecer na lista de processos. No primeiro contato o agente
só aceita o servidor se a cadeia TLS trouxer a CA com aquele SHA-256; depois, só confia nela.
Se o servidor usa certificado público (`serve --tls-cert`), o `enroll` vai sem `--ca-pin` e o
agente confere o servidor pelas CAs do sistema, como um navegador.

Quando o tenant muda de servidor (da nuvem para uma instalação própria, por exemplo), o agente
troca de endereço sem recadastrar:

```sh
invenire-agent set-server --server https://invenire.novo:8443 --ca-pin sha256:…   # sem pin = certificado público
```

O crachá é o mesmo, porque ele vem da CA do tenant, que vai junto com o tenant. Antes de gravar,
o `set-server` pergunta ao servidor novo (`GET /v1/agent`) se ele reconhece o crachá; se não
reconhecer, ou se o pin não conferir, nada muda. Depois, reinicie o serviço.

O estado fica em `%ProgramData%\Invenire` ou `/var/lib/invenire` (`--state-dir`): chave,
certificado, CA, `agent.json`, o hash do último snapshot (para o `prev_sha256`) e a fila em
`queue/`. No Windows o diretório passa a ter dono Administradores e acesso só de SYSTEM e
Administradores; no Linux, 0700.

A fila guarda até 100 snapshots (`--max-queue`) com os bytes exatos que serão enviados; o nome
de cada arquivo traz o SHA-256, conferido antes do envio. O que o servidor recusa de vez vai para
`queue/rejected/` com o motivo. Se o certificado for revogado, `run` para com o código de saída
3 e a fila fica intacta.

### Como serviço

Depois do `enroll`, no mesmo terminal de administrador ou root:

```sh
invenire-agent service install                 # --interval, --state-dir, --max-queue, --bin
invenire-agent service uninstall               # executável e estado ficam
```

`install` copia o executável para um lugar que só administradores alteram (`--bin`), registra o
serviço `invenire-agent` rodando `run` com as opções dadas e o inicia. Rodar `install` de novo
com um executável novo é a atualização: para o serviço, troca o executável e a configuração e
o inicia de novo.

| | Windows | Linux |
|---|---|---|
| Executável | `%ProgramFiles%\Invenire\invenire-agent.exe` | `/usr/local/bin/invenire-agent` |
| Registro | serviço automático (início atrasado), conta SYSTEM | `/etc/systemd/system/invenire-agent.service`, root, habilitada |
| Log | Visualizador de Eventos, Aplicativo, origem `invenire-agent` | `journalctl -u invenire-agent` |
| Se o processo cair | reinicia em 1, 5 e 15 min | reinicia em 60 s |
| Certificado revogado (saída 3) | para e não reinicia | para e não reinicia (`RestartPreventExitStatus`) |

A unit roda como root, mas com o sistema protegido: `ProtectSystem=full`, `ProtectHome=read-only`,
`NoNewPrivileges`, sem acesso a dispositivos, módulos ou tunables do kernel, e só sockets IP e
Unix. A unit é gerada a partir de `cmd/invenire-agent/invenire-agent.service.tmpl`.

Depois de mudar as structs de `pkg/snapshot`, regenere o schema:

```sh
go generate ./pkg/snapshot
```

## Servidor

```sh
export INVENIRE_DATABASE_URL="postgres://invenire:invenire@localhost:5442/invenire?sslmode=disable"
go build -o bin/ ./cmd/invenire-server
bin/invenire-server serve --listen :8443 --data data --hosts localhost,invenire.local,10.0.0.5
bin/invenire-server tenant create "Prefeitura"
bin/invenire-server token create --tenant "Prefeitura" --ttl 24h --description "lote 1"
bin/invenire-server actor create --tenant "Prefeitura" --name "athos" --scopes read,admin
```

`serve` aplica as migrações e, na primeira vez, cria em `data/` a CA da instalação (`ca/`) e a
chave-mestra (`master.key`). São duas confianças separadas:

| | Quem confere | Com o quê |
|---|---|---|
| O agente confere o servidor | o agente | a CA da instalação (`data/ca`), com o pin; ou, com `--tls-cert`/`--tls-key`, um certificado público pelas CAs do sistema |
| O servidor confere o agente | o servidor | a CA **do tenant** do agente, uma por tenant, independente, criada no primeiro cadastro |

A CA da instalação emite o certificado TLS do servidor a cada subida, para os nomes de `--hosts`:
todo nome ou IP que os agentes usam na URL do `enroll` precisa estar ali. Um certificado público
(`--tls-cert`, com a cadeia) é relido sozinho quando o arquivo muda, como na renovação do
certbot.

A CA de cada tenant fica no banco (`tenant_ca`), com a chave cifrada (AES-256-GCM) pela
chave-mestra. Ela pode vir de `INVENIRE_MASTER_KEY` (32 bytes em base64) em vez do arquivo.
**Guarde `data/master.key` e `data/ca/ca.key` junto com o backup do banco:** sem a chave-mestra o
servidor ainda confere os crachás emitidos, mas não emite novos; sem `ca.key` os agentes em
modo pin deixam de reconhecer o servidor. Como a CA do tenant não depende da instalação, levar
um tenant para outro servidor é levar a CA dele junto, e os agentes só trocam de endereço
(`set-server`).

Agentes cadastrados antes das CAs por tenant têm crachá da CA da instalação. Na subida, o `serve`
registra essa CA, só para conferir, em cada tenant que tem esses agentes; os cadastros novos já
saem da CA do tenant.

`token create` mostra o token de cadastro (uso único, só o SHA-256 fica no banco) e o pin
SHA-256 da CA da instalação, que o agente usa para conferir o servidor no primeiro contato. Com
certificado público, não há pin.

| Rota | Autenticação | Faz |
|---|---|---|
| `POST /v1/enroll` | token de cadastro | recebe CSR e dados do host; devolve o crachá, assinado pela CA do tenant, e a CA da instalação (vazia com certificado público) |
| `GET /v1/agent` | crachá de agente | diz de qual agente e ativo é o crachá; o `set-server` usa para conferir o servidor novo |
| `POST /v1/snapshots` | crachá de agente não revogado, conferido contra a CA do tenant que o emitiu | recebe o snapshot (gzip opcional) com o SHA-256 no cabeçalho `Invenire-Snapshot-SHA256`; 202 se novo, 200 se repetido |
| `GET /v1/software/unmapped` | token de API, escopo `read` | nomes sem produto nos ativos do tenant, dos mais comuns para os menos, com o palpite das regras (`?source=`, `?limit=`, `?cursor=`) |
| `PUT /v1/software/raw/{id}` | token de API, escopo `admin` | mapeamento manual: `{"vendor": "…", "product": "…"}` |
| `GET /healthz` | nenhuma | 204 se o banco responde |

Revogar um agente é preencher `agent.revoked_at`; a próxima requisição dele recebe 401. Revogar
a CA de um tenant (`tenant_ca.revoked_at`) derruba todos os crachás dela de uma vez; o cadastro
seguinte cria uma CA nova.

`actor create` cria um usuário ou pipeline (`--kind human|ai_agent|ci`) e mostra um token de
API (`inva_…`, enviado como `Authorization: Bearer`). Só o SHA-256 fica em `api_token`;
revogar é preencher `revoked_at`. As respostas trazem `as_of`, o último `change_log.id`
considerado. Na lista de não identificados, nome e fabricante vêm dentro de `collected`:
foram escritos na máquina inventariada e são dado, não instrução.

Cada snapshot é guardado duas vezes: `payload` (jsonb, para consultar) e `body` (os bytes
exatos, comprimidos com lz4). Um `CHECK` garante `sha256(body) = sha256`, e a consulta
`VerifySnapshotChain` reconfere hashes e encadeamento de um ativo só com o que está no banco.

### Ingestão

O `serve` roda a ingestão no mesmo processo. Ela acorda a cada snapshot novo e, sem aviso,
procura pendentes a cada 30s, inclusive os que chegaram antes de o servidor subir. Cada
snapshot é aplicado numa transação, em ordem de coleta:

1. Cada software vira um nome bruto em `software_raw`. Os ainda sem produto passam pelas
   regras de normalização (abaixo). Pacotes Linux levam o PURL na versão
   (`pkg:deb/ubuntu/curl@8.5.0-2ubuntu10.4`).
2. Hostname, SO, fabricante e modelo do ativo são atualizados, com `asset_changed` no
   `change_log` quando mudam.
3. O software identificado é comparado com as instalações abertas do ativo, pela chave
   versão + caminho. As novas abrem (`software_installed`), as presentes recebem
   `last_snapshot_id` e as ausentes fecham com `removed_at` (`software_removed`). Uma
   reinstalação abre uma linha nova, e a antiga fica no histórico.

Casos que não mexem no estado atual:

- **Software sem produto** não vira instalação. Fica na lista de não identificados até ser
  mapeado.
- **Coletor de software com erro** (`collector_errors`): a lista pode estar incompleta, então
  nada é fechado.
- **Snapshot fora de ordem** (coletado antes do último já aplicado): só alimenta
  `software_raw`, e o `change_log` registra `snapshot_out_of_order`.
- **Payload ilegível**: fica com o motivo em `snapshot.ingest_error` e não trava a fila.

Texto vindo da máquina (nomes, fabricante, versão, caminho, hostname) é gravado sem
caracteres de controle nem de formatação invisíveis e truncado: quem controla a máquina
controla esse texto, e ele chega a agentes de IA. Um snapshot com `\u0000` é recusado com 422
pelo `POST /v1/snapshots`, porque o jsonb não o guarda; o agente já tira NUL na coleta.

### Normalização

`internal/server/normalize` reconhece o produto de cada nome bruto, nesta ordem:

| Regra | Exemplo | Confiança |
|---|---|---|
| Catálogo (`catalog.json`) | "Google Chrome" (registro) e `google-chrome-stable` (dpkg) → Google Chrome | 1 |
| Pacote Linux | `curl` (dpkg ou rpm) → curl | 1 |
| Heurística do Windows | "Cisco Packet Tracer 8.2.2 64Bit" de "Cisco Systems, Inc." → Cisco Systems / Packet Tracer | 0,7 com fabricante, 0,5 sem |

Só o que atinge `--auto-map-confidence` (padrão 0,9) é mapeado sem revisão; abaixo disso, o
resultado aparece como `suggestion` na lista de não identificados. Mapeamento manual nunca é
mudado por regra.

Quando um nome é mapeado, por regra ou à mão, os ativos que o têm (`asset_software_raw`)
ficam com `remap_pending`, e a ingestão recalcula as instalações deles a partir do último
snapshot, sem esperar o próximo. Ao subir, o `serve` passa as regras de novo sobre todos os
nomes que elas podem mudar: um catálogo novo vale para o que já foi ingerido, e uma regra
retirada desfaz o que tinha mapeado.

Para acrescentar um produto ao catálogo, inclua uma entrada com `id`, `vendor`, `product`
(aceita `$1` dos grupos) e uma expressão por fonte em `match`. `publisher` exige o
fabricante, para nomes fáceis de imitar. Rode `go test ./internal/server/normalize`.

## Banco de dados

PostgreSQL 17 de desenvolvimento, exposto em `localhost:5442` (mude com `INVENIRE_PG_PORT`):

```sh
docker compose -f deploy/docker-compose.yml up -d --wait
go run github.com/pressly/goose/v3/cmd/goose@v3.29.0 -dir migrations postgres "postgres://invenire:invenire@localhost:5442/invenire?sslmode=disable" up
```

Depois de mudar `migrations/` ou `internal/server/store/queries/`, regenere o código (sqlc via Docker):

```sh
docker run --rm -v "$PWD:/src" -w /src sqlc/sqlc:1.30.0 generate
```

Os testes de `internal/server/store` usam um banco de verdade, num schema temporário, e são pulados sem a variável:

```sh
INVENIRE_TEST_DATABASE_URL="postgres://invenire:invenire@localhost:5442/invenire?sslmode=disable" go test ./...
```
