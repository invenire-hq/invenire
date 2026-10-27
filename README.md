# Invenire

Camada de inventário e contexto: o que existe, onde está e de que é feito.
A arquitetura e o modelo de dados estão no documento
[Invenire — Arquitetura e Modelo de Dados](https://claude.ai/artifact/WfzRB6TWaCNPQxdpx9QJtc).

## Estado (v0)

- [x] `pkg/snapshot` v1: structs Go, hash SHA-256 e JSON Schema gerado (`schema/v1.json`)
- [x] Coletores de host, SO e software: Windows (SMBIOS via `GetSystemFirmwareTable`, registro) e Linux (DMI, os-release, dpkg, rpm)
- [x] `invenire-agent collect --out` offline
- [ ] Migrações v0 e consultas sqlc
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
