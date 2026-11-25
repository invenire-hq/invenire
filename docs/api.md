Português | [English](en/api.md)

# API

A API fica em `/v1`, na mesma porta do painel. Há três tipos de autenticação:

- **token de cadastro** (`inv_…`), só no `POST /v1/enroll`;
- **crachá de agente** (mTLS ou assinatura; veja [seguranca.md](seguranca.md#o-agente-e-o-servidor));
- **token de API** (`inva_…`), enviado como `Authorization: Bearer inva_…`, com escopo `read`
  ou `admin`.

Tokens de API se criam no painel (**Acesso**) ou pela linha de comando:

```sh
invenire-server actor create --tenant "Minha Organização" --name "relatorios" --kind ci --scopes read
curl -H "Authorization: Bearer inva_…" https://invenire.exemplo.org/v1/assets
```

`--kind` é `human`, `ai_agent` ou `ci`. Só o SHA-256 do token fica em `api_token`; revogar é
preencher `revoked_at` (no painel, **Acesso**). No modo direto, o `curl` precisa do `-k` (ou de
`--cacert` com a CA da instalação).

## Rotas

| Rota | Autenticação | Faz |
|---|---|---|
| `POST /v1/enroll` | token de cadastro | recebe CSR e dados do host; devolve o crachá, assinado pela CA da organização, e a CA da instalação (vazia com certificado público) |
| `GET /v1/agent` | crachá de agente | diz de qual agente e ativo é o crachá; o `set-server` usa para conferir o servidor novo |
| `POST /v1/agent/renew` | crachá de agente | recebe um CSR e devolve um crachá novo, da CA que assina hoje para a organização; o crachá apresentado continua aceito até vencer |
| `POST /v1/snapshots` | crachá de agente não revogado | recebe o snapshot (gzip opcional) com o SHA-256 no cabeçalho `Invenire-Snapshot-SHA256`; `202` se novo, `200` se repetido; `422` se o `collected_at` estiver mais de 24h à frente do servidor |
| `GET /v1/software/unmapped` | API, `read` | nomes sem produto nos ativos da organização, dos mais comuns para os menos, com o palpite das regras (`?source=`, `?limit=`, `?cursor=`) |
| `PUT /v1/software/raw/{id}` | API, `admin` | mapeamento manual da organização: `{"vendor": "…", "product": "…"}`; devolve `product_scope` (`catalog` ou `tenant`) |
| `DELETE /v1/software/raw/{id}` | API, `admin` | desfaz o mapeamento manual da organização |
| `GET /v1/units` | API, `read` | a árvore de unidades, com `path` e `asset_count` |
| `POST /v1/units` | API, `admin` | cria unidade: `{"parent_path": "Sede", "kind": "setor", "name": "Financeiro"}` (ou `parent_id`; sem mãe, raiz) |
| `PATCH /v1/units/{id}` | API, `admin` | renomeia (`name`), muda o tipo (`kind`) ou move (`parent_id`; `null` = raiz) com tudo o que está abaixo |
| `DELETE /v1/units/{id}` | API, `admin` | apaga unidade vazia: sem subunidades, ativos nem token de cadastro válido |
| `PUT /v1/assets/{id}/unit` | API, `admin` | põe o ativo numa unidade: `{"unit_id": "…"}`; `null` tira de qualquer unidade |
| `GET /v1/assets/{id}/labels` | API, `read` | etiquetas do ativo |
| `PUT /v1/assets/{id}/labels/{chave}` | API, `admin` | cria ou troca a etiqueta: `{"value": "produção"}` |
| `DELETE /v1/assets/{id}/labels/{chave}` | API, `admin` | remove a etiqueta |
| `GET /v1/assets` | API, `read` | ativos em ordem de hostname, com unidade, etiquetas, agente ativo e quantos programas têm (filtros abaixo) |
| `GET /v1/assets/{id}` | API, `read` | um ativo, no mesmo formato |
| `GET /v1/assets/{id}/software` | API, `read` | o que está instalado agora, em ordem de produto, com `installed_since` (coleta que viu primeiro) e `last_seen_at` (`?product=` busca no produto e no fabricante) |
| `GET /v1/assets/{id}/history` | API, `read` | eventos do ativo no `change_log`: instalações, remoções, mudanças de atributo, de unidade e de etiqueta (`?since=2026-03-01`, `?kind=`, `?after=`) |
| `GET /v1/software` | API, `read` | produtos instalados agora, com quantos ativos e versões (`?product=`, `?unit=`) |
| `GET /v1/software/{id}/installations` | API, `read` | em quais ativos o produto está, com as versões e quantos ativos cada uma tem (`?version=`, `?version_id=`, `?unit=`) |
| `GET /v1/changes` | API, `read` | o fluxo de mudanças depois de um cursor (`?after=`, `?kind=a,b`) |
| `GET /v1/export` | API, `admin` | o pacote de exportação da organização (`?with_ca_key=true` inclui a chave da CA; veja [exportacao.md](exportacao.md)) |
| `GET /healthz` | nenhuma | `204` se o banco responde |

Fora de `/v1`, rotas públicas do servidor: `GET /download/agent/{windows-amd64|linux-amd64|linux-arm64}`,
`GET /download/SHA256SUMS`, `GET /download/instalar.ps1` e `GET /download/instalar.sh` (com o
endereço do servidor e o pin, se houver, já preenchidos). O resto é o painel.

## Consultas

Filtros de `GET /v1/assets`: `?unit=` (id, caminho como `Sede/Financeiro`, ou `none` para ativos
sem unidade; inclui as subunidades, a menos que `?subtree=false`), `?hostname=` (trecho),
`?os_family=windows`, `?label=ambiente=produção` (repetível: todas precisam bater) e
`?software_id=` (ativos com o produto instalado agora).

As listas vêm em páginas de 50 (`?limit=` até 200); a próxima página é `?cursor=` com o
`next_cursor` da anterior. No `change_log` o cursor é o id do último evento: quem acompanha
guarda o `next_cursor` e pergunta de novo com `?after=`; `more: true` diz que a página veio
cheia. Cada resposta é lida num só instante e traz o `as_of` desse instante (o último
`change_log.id` considerado).

O que foi escrito na máquina inventariada vem dentro de `collected` (nome e fabricante na lista
de não identificados; hostname, série, modelo e SO nos ativos; versão e caminho nas
instalações): é dado, não instrução. O `data` dos eventos do `change_log` também pode trazer
esse texto (nome bruto, versão, caminho).

## Eventos do `change_log`

Toda mudança vai para o `change_log` com ator e data. Alguns tipos: `agent_enrolled` (com
`revoked_agents`, quantos agentes anteriores da máquina foram revogados), `software_installed`,
`software_removed`, `asset_changed`, `snapshot_out_of_order`, `unit_created`, `unit_updated`,
`unit_deleted`, `asset_moved` (com `from_unit_id` e `to_unit_id`, o que responde "onde estava
esta máquina em março?"), `label_set`, `label_removed`, `enrollment_token_created`,
`enrollment_token_revoked`, `enrollment_package_downloaded` e `tenant_exported`.
