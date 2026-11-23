# Desenvolvimento

Go (a versão de `go.mod`), Docker (para o PostgreSQL e o sqlc) e Git. Os comandos são do shell
na pasta do repositório; no PowerShell, troque `export X=…` por `$env:X="…"` e `bin/` por
`bin\`.

## Estrutura

| Pasta | O que tem |
|---|---|
| `cmd/invenire-server`, `cmd/invenire-agent` | os dois programas (linha de comando) |
| `internal/server/api` | rotas `/v1`, autenticação do agente (mTLS e assinatura), proxy, ações do painel |
| `internal/server/web` | painel web (templates e estáticos embutidos com `go:embed`, catálogo de mensagens) |
| `internal/server/download` | distribuição do agente (`/download/`) e o pacote do instalador |
| `internal/server/ingest`, `normalize` | ingestão dos snapshots e regras de normalização de software |
| `internal/server/store` | consultas SQL (`queries/`) e o código gerado pelo sqlc |
| `internal/server/pki`, `export` | CAs e chave-mestra; exportação da organização |
| `internal/agent/…` | coleta, fila, transporte e estado do agente |
| `internal/wire` | o que agente e servidor compartilham (cabeçalhos, mensagem assinada) |
| `pkg/snapshot` | o formato do snapshot (structs, hash, JSON Schema em `schema/v1.json`) |
| `pkg/sysinfo` | leitura de baixo nível de host, SO e software (Windows e Linux), sem depender do formato do snapshot |
| `migrations` | migrações do banco (goose), embutidas no servidor |
| `deploy/servidor` | compose de produção, Caddyfile, exemplo de Nginx, backup e restauração |
| `deploy/agente` | instaladores do agente (embutidos no servidor para o pacote) |
| `deploy/docker-compose.yml` | PostgreSQL de desenvolvimento |
| `scripts/build.sh` | monta uma versão para distribuir |

## Banco de desenvolvimento

PostgreSQL 17, exposto em `localhost:5442` (mude com `INVENIRE_PG_PORT`):

```sh
docker compose -f deploy/docker-compose.yml up -d --wait
```

O servidor aplica as migrações ao subir. Para aplicá-las à mão:

```sh
go run github.com/pressly/goose/v3/cmd/goose@v3.29.0 -dir migrations postgres "postgres://invenire:invenire@localhost:5442/invenire?sslmode=disable" up
```

## Testes

Os testes que usam banco rodam num schema temporário e são pulados sem a variável:

```sh
export INVENIRE_TEST_DATABASE_URL="postgres://invenire:invenire@localhost:5442/invenire?sslmode=disable"
go test ./...
```

## Código gerado

Depois de mudar `migrations/` ou `internal/server/store/queries/`, regenere o código do sqlc:

```sh
docker run --rm -v "$PWD:/src" -w /src sqlc/sqlc:1.30.0 generate
```

Depois de mudar as structs de `pkg/snapshot`, regenere o JSON Schema:

```sh
go generate ./pkg/snapshot
```

## Do zero à primeira máquina, sem Docker

O caminho manual, para desenvolver ou entender cada peça. Para instalar de verdade, use os
[roteiros](instalar/escolha.md).

```sh
docker compose -f deploy/docker-compose.yml up -d --wait
go build -o bin/ ./cmd/invenire-server ./cmd/invenire-agent
scripts/build.sh dev        # executáveis do agente para o painel distribuir (dist/dev/)
export INVENIRE_DATABASE_URL="postgres://invenire:invenire@localhost:5442/invenire?sslmode=disable"
bin/invenire-server serve --listen :8443 --data data --hosts localhost,127.0.0.1 --agent-dir dist/dev
```

Na primeira subida o servidor cria `data/ca/` e `data/master.key` e mostra no log o código da
primeira configuração. Abra `https://localhost:8443/` e siga o painel, ou faça tudo pela linha
de comando, em outro terminal com a mesma `INVENIRE_DATABASE_URL`:

```sh
bin/invenire-server tenant create "Minha Organização"
bin/invenire-server unit create --tenant "Minha Organização" --kind site "Sede"
bin/invenire-server unit create --tenant "Minha Organização" --parent "Sede" --kind setor "Financeiro"
bin/invenire-server token create --tenant "Minha Organização" --unit "Sede/Financeiro" --ttl 24h --description "lote 1"
```

O `token create` mostra duas coisas diferentes, e o `enroll` pede as duas:

| | Parece com | Para que serve |
|---|---|---|
| **token** | `inv_9LYq…` | senha de uso limitado: autoriza **uma** máquina (ou `--uses N`) a se cadastrar até o `--ttl` |
| **ca pin** | `sha256:a6c6…` (64 dígitos hex) | impressão digital do servidor: o agente só fala com o servidor cuja CA tem esse SHA-256. Não é segredo |

Como administrador (Windows) ou root (Linux):

```sh
bin/invenire-agent enroll --server https://localhost:8443 --token inv_COLE_O_TOKEN --ca-pin sha256:COLE_O_PIN
bin/invenire-agent service install
bin/invenire-server status --tenant "Minha Organização"
```

Para consultar pela API:

```sh
bin/invenire-server actor create --tenant "Minha Organização" --name "dev" --scopes read,admin
curl -k -H "Authorization: Bearer inva_COLE_O_TOKEN" https://localhost:8443/v1/assets
```

## Versão para distribuir

```sh
scripts/build.sh 0.1.0
```

Gera `dist/0.1.0/` com o agente para Windows e Linux (amd64 e arm64), o servidor, os scripts de
instalação e o `SHA256SUMS`. A pasta serve de `--agent-dir` para o servidor fora do Docker e de
pasta da versão para instalar o agente à mão ([agentes.md](agentes.md#3-manual)).

A imagem Docker do servidor (que já leva os executáveis do agente):

```sh
docker build --build-arg VERSION=0.1.0 -t invenire-server:0.1.0 .
```

## Integração contínua e publicação

No GitHub Actions (`.github/workflows/`):

- **`ci.yml`**, a cada push e pull request: `gofmt`, `go vet` (Linux e Windows), código do sqlc
  em dia (`sqlc diff`), `go build`, `go test ./...` com um PostgreSQL de serviço, e a imagem
  Docker montando.
- **`release.yml`**, a cada tag `v*`: roda o `ci.yml`; monta os executáveis com
  `scripts/build.sh` e os anexa à release do GitHub com o `SHA256SUMS`, usando como notas a seção
  da versão no `CHANGELOG.md`; e publica `ghcr.io/invenire-hq/invenire-server` para
  `linux/amd64` e `linux/arm64`, com as etiquetas `X.Y.Z`, `X.Y` e `latest` (esta só em versão
  final; `v0.2.0-rc.1` sai como pré-lançamento).

Para lançar uma versão:

1. No `CHANGELOG.md`, renomeie "Não lançado" para `## [X.Y.Z]`, abra um "Não lançado" vazio
   acima e acerte os links do fim.
2. Commit, tag anotada e push da tag:

   ```sh
   git tag -a v0.1.0 -m "Invenire 0.1.0"
   git push origin v0.1.0
   ```

As actions estão fixadas por SHA (com a versão no comentário). Para atualizar uma, troque o SHA
pelo da tag nova (`git ls-remote https://github.com/<dono>/<action> refs/tags/<versão>`).

Para conferir os workflows sem o GitHub:

```sh
docker run --rm -v ":/repo" -w /repo rhysd/actionlint:1.7.7
```

## Catálogo de software

Para acrescentar um produto ao catálogo de normalização, inclua uma entrada em
`internal/server/normalize/catalog.json` com `id`, `vendor`, `product` (aceita `$1` dos grupos) e
uma expressão por fonte em `match`. `publisher` exige o fabricante, para nomes fáceis de imitar.
Rode `go test ./internal/server/normalize`.

## Painel

Textos em `internal/server/web/messages.go`; `TestCatalogHasEveryKey` acusa chave usada nos
templates e faltando no catálogo. Texto vindo das máquinas é dado, não HTML: nada no painel usa
`template.HTML`.
