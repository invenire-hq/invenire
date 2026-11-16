# Formato de exportação do tenant (versão 1)

O pacote de exportação leva um tenant inteiro de um servidor Invenire para outro: da nuvem para
uma instalação própria, de volta, ou para um arquivo de guarda. É o `git clone` do Invenire. O
formato é contrato público: campos novos podem aparecer numa versão compatível; renomear um
campo ou mudar o sentido dele exige `format_version` nova. As structs Go que o definem estão em
[`internal/server/export/format.go`](../internal/server/export/format.go).

```sh
invenire-server tenant export --tenant "Minha Organização" --out minha-organizacao.invenire.tar.gz [--with-ca-key]
invenire-server tenant verify minha-organizacao.invenire.tar.gz [--export-key <chave>]
curl -H "Authorization: Bearer inva_…" -OJ "https://invenire.exemplo.org/v1/export?with_ca_key=true"   # escopo admin
```

No painel, a mesma exportação está em **Exportar** (administrador). Na instalação com Docker, o
`tenant export` roda dentro do contêiner e grava o pacote lá; a API ou o painel entregam o
arquivo direto.

## O que vai e o que não vai

Vai o que não pode ser recalculado. Fica de fora o que é derivado dos snapshots e o que é
segredo da instalação.

| Vai | Não vai |
|---|---|
| snapshots (os bytes exatos que o agente enviou) e seus metadados | instalações (`installation`): recalculadas dos snapshots |
| ativos, agentes, unidades, etiquetas | nomes brutos e mapeamentos de regra: recalculados pelas regras de quem importa |
| mapeamentos manuais do tenant e os produtos que ele criou | o catálogo comum: cada servidor tem o seu |
| atores (pessoas, IA, pipelines), propostas | tokens de API e de cadastro: quem importa emite novos |
| o `change_log` inteiro do tenant | a chave-mestra da instalação |
| as CAs do tenant (certificados) e, com `--with-ca-key`, as chaves | |

## Estrutura

Um `.tar.gz` com estas entradas, nesta ordem, sem diretório raiz:

| Entrada | Conteúdo |
|---|---|
| `tenant_ca.jsonl` | CAs que assinaram crachás de agentes do tenant |
| `units.jsonl` | unidades (a mãe pode vir depois da filha) |
| `assets.jsonl` | ativos; `collected` é o que a máquina reportou no último snapshot ingerido |
| `labels.jsonl` | etiquetas |
| `agents.jsonl` | agentes, ativos e revogados, com o fingerprint do crachá e a CA que o emitiu |
| `actors.jsonl` | atores, sem tokens |
| `snapshots.jsonl` | metadados de cada snapshot e o caminho dos bytes |
| `products.jsonl` | produtos (e fabricantes) criados à mão pelo tenant |
| `mappings.jsonl` | mapeamentos manuais: o nome bruto por extenso e o produto |
| `proposals.jsonl` | propostas de agentes de IA e as decisões |
| `changes.jsonl` | o `change_log` do tenant, em ordem de id |
| `snapshots/<sha256>.json` | os bytes exatos de cada snapshot; o nome é o SHA-256 do conteúdo |
| `snapshots/<sha256>.payload.json` | só para snapshots recebidos antes de o servidor guardar os bytes: o conteúdo reescrito pelo banco, sem SHA-256 reconferível |
| `manifest.json` | por último: o que há no pacote e como conferir |

Arquivos `.jsonl` têm um objeto JSON por linha, terminada em `\n`, em UTF-8. Datas em RFC 3339
com fuso, ids em UUID (os mesmos do servidor de origem: não colidem ao importar), hashes em hex
minúsculo e bytes (`key_sealed`) em base64.

## manifest.json

```json
{
  "format": "invenire-tenant-export",
  "format_version": 1,
  "tenant": {"id": "35ff3283-…", "name": "Minha Organização"},
  "exported_at": "2026-11-10T14:00:28Z",
  "server_version": "0.3.0",
  "as_of": 421,
  "files": {"assets.jsonl": {"sha256": "…", "bytes": 1234, "records": 1}, "…": {}},
  "snapshot_bodies": 2,
  "snapshot_payloads": 0,
  "ca_key": {"algorithm": "AES-256-GCM", "aad": "tenant_id||tenant_ca_id"}
}
```

- `as_of`: o último `change_log.id` do tenant quando o pacote foi lido. Tudo foi lido numa só
  transação, nesse instante.
- `files`: SHA-256, tamanho e linhas de cada `.jsonl`.
- `ca_key`: só existe se o pacote leva alguma chave de CA.

## Como conferir

O `tenant verify` faz isto, sem banco, e qualquer implementação pode refazer:

1. `format` e `format_version` conhecidos.
2. Cada `.jsonl` presente, com o SHA-256 e o número de linhas do manifesto; nenhum arquivo além
   dos listados e de `snapshots/`.
3. O SHA-256 de cada `snapshots/<sha256>.json` é o do nome, e cada um corresponde a uma linha
   de `snapshots.jsonl` (e vice-versa).
4. Por ativo, em ordem de `collected_at` e `received_at`, o `prev_sha256` de cada snapshot é o
   `sha256` do anterior. Um elo divergente **não** invalida o pacote: o servidor aceita o
   snapshot e registra `snapshot_chain_broken`, e o pacote preserva a divergência como ela foi.
5. Referências: unidade da mãe, do ativo e da etiqueta; ativo e CA do agente; ativo e agente
   do snapshot; produto do tenant de cada mapeamento manual.
6. Cada certificado de CA é válido e tem o `fingerprint` declarado; com a chave de exportação,
   cada chave abre e é a do certificado.

O SHA-256 do arquivo inteiro, mostrado no `export` e no `verify`, é a impressão digital do
pacote: anote-a para provar depois que é o mesmo arquivo.

## A chave da CA

Os crachás dos agentes são assinados pela CA do tenant. Levando a chave dela, o servidor novo
continua emitindo e renovando crachás, e os agentes só trocam de endereço
(`invenire-agent set-server`). Sem a chave, o servidor novo confere os crachás atuais, mas os
agentes precisam ser recadastrados antes de o crachá vencer.

Com `--with-ca-key` (ou `?with_ca_key=true`), a chave privada de cada CA não revogada sai
cifrada em `key_sealed`:

- AES-256-GCM, com uma **chave de exportação** de 32 bytes gerada na hora;
- nonce de 12 bytes no início do cifrado;
- dados adicionais: os 16 bytes do id do tenant seguidos dos 16 bytes do id da CA.

A chave de exportação não vai no pacote. O `tenant export` a mostra uma vez; a API a devolve no
cabeçalho `Invenire-Export-Key`. Guarde-a separada do pacote: com os dois, alguém emite
crachás em nome do tenant. CAs só de conferência (a CA antiga da instalação, de agentes
cadastrados antes das CAs por tenant) não têm chave.

## Auditoria

Toda exportação é registrada no `change_log` do tenant como `tenant_exported`, com o `as_of`,
se levou a chave da CA e por onde saiu (`cli` ou `api`). O registro entra depois da
exportação, então não aparece no próprio pacote.

## Importar

Ainda não implementado. Quem importa deve:

1. conferir o pacote como acima;
2. recusar se o tenant (pelo id) já existir;
3. gravar os registros com os mesmos ids e cada snapshot com os bytes originais;
4. para cada mapeamento manual, reencontrar ou criar o nome bruto pela fonte, nome e
   fabricante, e ligar ao produto: os de `product_scope` `tenant` vêm de `products.jsonl`; os de
   `catalog` são procurados pelo nome no catálogo de quem importa;
5. passar todos os snapshots pela ingestão, em ordem de coleta, para recalcular as instalações.
