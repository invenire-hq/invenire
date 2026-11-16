# Como o Invenire funciona por dentro

São três programas: o **PostgreSQL**; o **servidor** (`invenire-server`), que recebe, organiza e
mostra os inventários (API em `/v1` e painel web na mesma porta); e o **agente**
(`invenire-agent`), instalado em cada máquina. A arquitetura e o modelo de dados estão no
documento
[Invenire — Arquitetura e Modelo de Dados](https://claude.ai/artifact/WfzRB6TWaCNPQxdpx9QJtc).

## Organizações, unidades e etiquetas

A **organização** (tenant) é o limite rígido: outra organização, outro contrato, outra CA. Um
servidor pode ter várias; cada pessoa do painel vê só as suas. Dentro dela, a organização das
máquinas é uma camada à parte, ligada a cada ativo:

- **Unidade**: árvore livre, com a profundidade e os nomes de camada que se quiser
  (Sede → Financeiro → Recepção; Filial Centro → Andar 2). Cada ativo está em no máximo uma
  unidade. Na linha de comando, a unidade é indicada pelo caminho, com `/` entre os níveis, por
  isso o nome não pode ter `/`.
- **Etiqueta**: o que não é hierarquia, várias por ativo: `ambiente=produção`,
  `dados=pessoais`. A chave tem minúsculas, dígitos, `_`, `.` e `-`; o valor, até 256
  caracteres.

A máquina cadastrada com um token de unidade já entra nela. Recadastrar uma máquina que já tem
unidade não a tira do lugar: quem escolheu a unidade dela foi uma pessoa. O banco garante, por
chave estrangeira com o `tenant_id`, que ativo, token e unidade são sempre da mesma organização.

## Ingestão

O servidor roda a ingestão no mesmo processo. Ela acorda a cada snapshot novo e, sem aviso,
procura pendentes a cada 30 segundos, inclusive os que chegaram antes de o servidor subir. Cada
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

Texto vindo da máquina é gravado sem caracteres de controle nem de formatação invisíveis e
truncado ([seguranca.md](seguranca.md#texto-vindo-das-máquinas)).

## Normalização

`internal/server/normalize` reconhece o produto de cada nome bruto, nesta ordem:

| Regra | Exemplo | Confiança |
|---|---|---|
| Catálogo (`catalog.json`) | "Google Chrome" (registro) e `google-chrome-stable` (dpkg) → Google Chrome | 1 |
| Pacote Linux | `curl` (dpkg ou rpm) → curl | 1 |
| Heurística do Windows | "Cisco Packet Tracer 8.2.2 64Bit" de "Cisco Systems, Inc." → Cisco Systems / Packet Tracer | 0,7 com fabricante, 0,5 sem |

Só o que atinge `--auto-map-confidence` (padrão 0,9) é mapeado sem revisão; abaixo disso, o
resultado aparece como sugestão na lista de não identificados (no painel, **Software** > não
identificados).

**Catálogo comum e produtos da organização.** O que as regras produzem é o catálogo comum,
igual para todas as organizações (`vendor` e `software` com `tenant_id` nulo;
`software_raw.software_id` guarda só mapeamento de regra). O mapeamento manual fica em
`tenant_software_map`: vale só para os ativos da organização de quem mapeou e vence a regra, que
nunca o muda. Se o produto ou o fabricante pedido já está no catálogo comum, ele é reaproveitado
(`product_scope: "catalog"`); se não está, é criado só para a organização
(`product_scope: "tenant"`), e o nome de um sistema interno não aparece para as outras. Desfazer
o mapeamento manual volta a valer a regra, se houver, ou devolve o nome à lista de não
identificados.

Quando um nome é mapeado, por regra ou à mão, os ativos que o têm (`asset_software_raw`; no
manual, só os da organização) ficam com `remap_pending`, e a ingestão recalcula as instalações
deles a partir do último snapshot, sem esperar o próximo. Ao subir, o servidor passa as regras de
novo sobre todos os nomes que elas podem mudar: um catálogo novo vale para o que já foi ingerido,
e uma regra retirada desfaz o que tinha mapeado.

Para acrescentar um produto ao catálogo, veja [desenvolvimento.md](desenvolvimento.md#catálogo-de-software).

## Agentes e revogação

Cada ativo tem um agente: cadastrar de novo a mesma máquina revoga o agente anterior, e o
`agent_enrolled` do `change_log` traz quantos foram revogados (`revoked_agents`). Revogar um
agente é preencher `agent.revoked_at` (no painel, **Máquinas** > revogar); a próxima requisição
dele recebe `401`, e ele para.

## Exportação

A organização inteira sai num pacote `.tar.gz` com os bytes exatos que os agentes enviaram, e
pode ser conferida sem banco. Formato e uso em [exportacao.md](exportacao.md).
