# Invenire

Camada de inventário e contexto: o que existe, onde está e de que é feito.
A arquitetura e o modelo de dados estão no documento
[Invenire — Arquitetura e Modelo de Dados](https://claude.ai/artifact/WfzRB6TWaCNPQxdpx9QJtc).

## Estado (v0)

- [x] `pkg/snapshot` v1: structs Go, hash SHA-256 e JSON Schema gerado (`schema/v1.json`)
- [x] `pkg/sysinfo`: leitura de baixo nível de host, SO e software — Windows (SMBIOS via `GetSystemFirmwareTable`, registro) e Linux (DMI, os-release, dpkg, rpm). Só depende de `golang.org/x/sys` e não conhece o formato do snapshot, para poder virar o módulo compartilhado com o [PeritiaGo](https://github.com/athosbes/PeritiaGo)
- [x] `invenire-agent collect --out` offline
- [x] Migrações v0 (goose), incluindo `actor`, `proposal` e `change_log`, e consultas sqlc em `internal/server/store`
- [ ] `POST /v1/enroll` e `POST /v1/snapshots` com mTLS
- [ ] Ingestão com diff de instalações
- [ ] Regras de normalização e lista de software não identificado

## Uso

```sh
go build -o bin/ ./cmd/invenire-agent
bin/invenire-agent collect --out snapshot.json
```

No Linux, rode como root: `product_uuid` e `product_serial` só são legíveis por ele.

Depois de mudar as structs de `pkg/snapshot`, regenere o schema:

```sh
go generate ./pkg/snapshot
```

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
