# Plano: instalação simples, painel web e documentação para código aberto

Plano para outra sessão implementar. Escrito em 2026-11-12, sobre o commit `53ec610`
(pacote do piloto pronto). As cinco fases foram implementadas; o estado de cada uma, com o que
ainda não foi testado de verdade, está em [Estado das fases](#estado-das-fases).

## Objetivo

Quem instala o agente numa máquina não deve precisar ser técnico: ou cola um comando, ou dá
dois cliques num pacote baixado do painel. Quem sobe o servidor é técnico, mas deve conseguir
só com a documentação, em três cenários: rede local, VPS e atrás de proxy reverso. O projeto
vai ser aberto, então nomes, exemplos e documentação precisam servir a qualquer organização.

## Decisões já tomadas (pelo Athos)

| Tema | Decisão |
|---|---|
| Painel web no servidor | **Sim.** Primeira configuração e uso do dia a dia pelo navegador |
| Instalador pré-configurado do agente | **Sim.** Gerado pelo painel, com endereço, pin e token |
| Janela gráfica no agente Windows | **Não agora.** Fica registrada em "Futuro" |
| Assistente interativo do servidor | **Não.** Servidor é para técnico; basta documentar bem |
| Proxy do teste na instituição | **Nginx simples**, terminando TLS. Os outros cenários precisam ao menos estar explicados (compatível ou não, e por quê) |
| Servidor hospedado | Documentar VPS agora. Um serviço pago com autocadastro vem depois: **não construir, mas não fechar a porta** |
| Licença | **AGPL-3.0** (decidido em 2026-11-17), `AGPL-3.0-only` nos cabeçalhos SPDX. Contribuições sob CLA + DCO, para manter serviço hospedado e licença comercial possíveis |
| Onde o código fica | Organização **`invenire-hq`** no GitHub: `github.com/invenire-hq/invenire`, imagem `ghcr.io/invenire-hq/invenire-server` |
| Autenticação do agente atrás de proxy | **Assinatura de requisição** (abordagem da Fase 1), confirmada |

## Estado atual que motiva o plano

- Administração só por CLI dentro do contêiner (`docker compose exec server invenire-server …`).
- Agente instala por `instalar.ps1`/`instalar.sh`, com `-Server`, `-CaPin` e token digitados, a
  partir de uma pasta gerada por `scripts/build.sh` (exige Go e um compartilhamento de rede).
- O agente se autentica por certificado de cliente no TLS (`internal/server/api/api.go`:
  `ClientAuth: tls.RequestClientCert`, leitura de `r.TLS.PeerCertificates`). **Um proxy que
  termina o TLS quebra isso**: o certificado não chega ao servidor.
- O servidor só fala TLS, com certificado da CA da instalação (modo pin) ou `--tls-cert`.
- Não há login de pessoa: só token de API (`inva_…`) criado por `actor create`.
- README é longo e voltado a quem desenvolve; `docs/piloto.md` usa "Prefeitura" em tudo.
- Sem `LICENSE`, sem remoto git, sem CI, sem binários publicados.

## Fase 1 — Servidor atrás de proxy (autenticação do agente por assinatura)

**Problema.** Com TLS terminado no Nginx, o crachá do agente não chega ao servidor.

**Abordagem recomendada: assinar cada requisição com a chave do crachá.** O agente passa a
mandar, sempre, além do certificado no TLS:

- `Invenire-Agent-Cert`: o crachá (DER em base64);
- `Invenire-Timestamp`: hora do agente (RFC 3339);
- `Invenire-Signature`: assinatura, com a chave do crachá, de
  `método \n caminho \n timestamp \n sha256(corpo)`.

O servidor confere a assinatura com a chave pública do crachá e depois faz exatamente o que
faz hoje com `PeerCertificates` (fingerprint → `GetAgentForAuth` → `pki.VerifyAgentCert`
contra a CA do tenant → revogação). Vale para `/v1/agent`, `/v1/agent/renew` e `/v1/snapshots`.

Por que esta e não repassar o certificado num cabeçalho do proxy (`$ssl_client_escaped_cert`):

- funciona com **qualquer** proxy HTTP com um `proxy_pass` comum, inclusive Nginx Proxy
  Manager sem configuração avançada e Cloudflare Tunnel, que não repassam certificado de cliente;
- o servidor não precisa confiar no proxy para saber quem é o agente (o proxy não tem a chave);
- o Nginx não precisa pedir certificado de cliente, então o navegador de quem abre o painel
  não mostra seletor de certificado (comum no Brasil, com e-CPF/e-CNPJ instalados);
- serve ao serviço hospedado futuro, atrás de balanceador ou CDN.

**Mudanças.**

- `internal/agent/transport`: assinar toda requisição autenticada. Sem configuração nova no
  agente; ele não precisa saber se há proxy.
- `internal/server/api`: middleware de autenticação do agente aceita assinatura **ou** mTLS.
  A verificação da cadeia e da revogação é uma só, compartilhada.
- `invenire-server serve --behind-proxy`: escuta HTTP puro (TLS é do proxy), não emite
  certificado da CA da instalação, `POST /v1/enroll` devolve a CA vazia (como já faz com
  `--tls-cert`), e só aceita assinatura. Exigir `--trusted-proxies <CIDR,…>`: só dessas
  origens se lê `X-Forwarded-For`/`X-Forwarded-Proto`; recusar subir sem a lista.
- O agente, atrás de proxy com certificado público, cadastra sem `--ca-pin` (já existe).

**Cuidados de projeto (decidir na implementação e documentar em `docs/seguranca.md`).**

- Relógio errado é um problema conhecido do piloto. Janela de tolerância larga no timestamp
  (sugestão: a mesma regra de 24h do `collected_at`) e sem nonce: repetir um snapshot já é
  inofensivo (200 "repetido") e repetir um `renew` devolve um crachá para a mesma chave.
  Registrar esse raciocínio; se a implementação achar um caso de repetição danoso, usar nonce.
- A assinatura cobre o corpo **como enviado** (gzip incluído). Conferir limite de tamanho antes
  de ler o corpo.
- No modo direto (sem proxy), manter mTLS aceito para não quebrar agentes já instalados. Avaliar
  depois do piloto se o servidor deixa de pedir certificado de cliente (elimina o seletor de
  certificado do navegador também no modo direto).

**Pronto quando:** teste de integração com um proxy HTTP no meio (httptest com
`httputil.ReverseProxy` terminando TLS) cobre cadastro, envio de snapshot, renovação e agente
revogado; assinatura adulterada, timestamp fora da janela e cabeçalho vindo de fora de
`--trusted-proxies` são recusados; os testes de mTLS atuais continuam passando.

## Fase 2 — Painel web

**Forma.** Dentro do próprio `invenire-server`, embutido com `go:embed`: HTML renderizado no
servidor (`html/template`) e JavaScript mínimo, sem etapa de build com Node. Mantém o binário
único e a imagem distroless, e é fácil de contribuir. Textos num catálogo de mensagens
(pt-BR agora) para permitir outro idioma depois. Mesma porta da API, sob `/` (a API continua
em `/v1`).

**Login de pessoa (não existe hoje).**

- Migração nova: credencial de login (e-mail único **na instalação inteira**, senha com
  argon2id) ligada a um `actor` humano, e tabela de sessões (só o hash do identificador).
  E-mail global, e não por tenant, para um único endereço servir várias organizações no
  serviço hospedado futuro.
- Cookie de sessão `HttpOnly`, `Secure`, `SameSite=Lax`; proteção CSRF nos formulários; limite
  de tentativas de login; expiração e "sair".
- Escopos do `actor` (`read`, `admin`) valem no painel como valem na API. Toda ação do painel
  passa pelas mesmas funções das rotas `/v1` e grava no `change_log` com o ator.
- CLI de recuperação: `invenire-server user set-password --email …` (quem tem acesso ao
  servidor recupera a conta; não há envio de e-mail nesta fase).

**Primeira configuração.** Sem nenhum tenant no banco, `/` leva a `/setup`, que pede um
**código de configuração** mostrado uma vez no log do `serve` (ou dado em
`INVENIRE_SETUP_CODE`). Sem isso, quem achar primeiro uma instalação nova exposta na internet
toma posse dela. O assistente cria a organização e o primeiro administrador e, em seguida,
oferece criar a primeira unidade e o primeiro token de cadastro.

**Telas, na ordem de valor para o piloto.**

1. **Visão geral**: o que o `status` mostra hoje (reaproveitar `internal/server/api/status.go`
   e as consultas de `status.sql`): máquinas por estado, as que pedem atenção primeiro, com a
   coluna "o que fazer" da tabela de `docs/piloto.md`.
2. **Adicionar máquinas**: criar token (unidade, quantas máquinas, validade, descrição), listar
   e revogar. Ao criar, mostra as formas de instalar da Fase 3. O token aparece uma vez só.
3. **Máquinas**: lista com filtros (unidade, sistema, nome), detalhe com software instalado e
   histórico; mudar de unidade; etiquetas; revogar o agente.
4. **Unidades**: árvore, criar, renomear, mover, apagar.
5. **Software**: produtos instalados e onde; não identificados com a sugestão das regras e o
   mapeamento manual.
6. **Acesso**: usuários do painel e tokens de API (criar, revogar).
7. **Exportar** a organização (com o aviso sobre a chave de exportação).

Texto vindo das máquinas (`collected`) é dado, não HTML: sempre escapado pelo template, nunca
`template.HTML`.

**Para não fechar a porta do serviço hospedado** (não implementar agora):

- nenhuma tela ou consulta assume um tenant único: o tenant vem sempre da sessão;
- criar organização + primeiro administrador é **uma função** usada pelo `/setup` e pela CLI,
  para um autocadastro futuro chamar a mesma;
- não misturar "administrador da organização" com "operador da instalação" (este continua
  sendo quem tem a CLI);
- deixar fora: autocadastro público, envio de e-mail, cobrança, limites por organização.

**Pronto quando:** numa instalação vazia, pelo navegador e sem terminal, dá para configurar,
criar um token, ver a máquina aparecer e acompanhar o estado; um usuário `read` não consegue
alterar nada; um usuário de um tenant não vê nada de outro (teste com dois tenants); as rotas
`/v1` e os testes atuais seguem iguais.

## Fase 3 — Instalador pré-configurado do agente

O servidor passa a distribuir o agente, para ninguém precisar de Go nem de compartilhamento.

- **Binários na imagem.** O `Dockerfile` compila também o agente (windows/amd64, linux/amd64,
  linux/arm64) e os guarda na imagem com `SHA256SUMS`. Rotas públicas (o binário não é
  segredo): `GET /download/agent/{so}-{arq}` e `GET /download/SHA256SUMS`.
- **Na tela do token, três formas, nesta ordem:**
  1. **Pacote para baixar** (o caminho "de clicar"): `.zip` para Windows e `.tar.gz` para Linux,
     gerado na hora para a sessão logada, com o agente, o script e um arquivo de configuração
     (endereço do servidor, pin se houver, token). No Windows inclui `Instalar.cmd`, que pede
     elevação (UAC) e chama o `instalar.ps1` com `-ExecutionPolicy Bypass`: a pessoa extrai e
     dá dois cliques. Funciona em qualquer modo de TLS, inclusive pin, e offline via pendrive.
  2. **Comando de uma linha** (só com certificado público, porque o download precisa confiar
     no servidor): PowerShell e shell, com o token numa variável de ambiente e **nunca na
     URL** (URL vai para log de proxy). O script servido já traz endereço e pin.
  3. **Manual**: os comandos de hoje, para quem quer ver cada passo.
- **Scripts.** `deploy/agente/instalar.ps1` e `instalar.sh` passam a ler o arquivo de
  configuração ao lado deles quando os parâmetros não vêm, a baixar o agente do servidor quando
  ele não está na pasta, e continuam conferindo o `SHA256SUMS`. Mensagens de erro em linguagem
  de leigo, com o que fazer. O pacote deve apagar ou avisar sobre o arquivo com o token depois
  de instalar.
- **Aviso no painel e na documentação:** o pacote contém o token; vale o mesmo cuidado (prazo
  curto, número de usos, revogar se vazar). Binário sem assinatura de código dispara
  SmartScreen e alguns antivírus: documentar o que aparece e como liberar.

**Pronto quando:** numa VM Windows e numa Linux limpas, baixar o pacote do painel e executar
instala e a máquina aparece `em-dia`, sem digitar endereço, pin ou token; o comando de uma
linha funciona atrás do Nginx com certificado público; rodar de novo não recadastra.

## Fase 4 — Implantação e documentação

**Arquivos de implantação.** Trocar `deploy/piloto/` por `deploy/servidor/` (manter backup e
restauração), usando a imagem publicada por padrão e `build:` como alternativa:

| Arquivo | Cenário |
|---|---|
| `compose.yml` | Rede local, sem domínio: porta 8443 direta, CA da instalação, agentes com pin |
| `compose.proxy.yml` | Atrás de um proxy que já existe: `--behind-proxy`, porta só em `127.0.0.1` |
| `compose.caddy.yml` | VPS do zero: Caddy na frente com Let's Encrypt automático; basta informar o domínio |
| `nginx/invenire.conf` | Bloco `server` de exemplo para o Nginx simples (o teste da instituição) |

**Documentação.** README curto (o que é, uma imagem do painel, início em cinco minutos, links).
O conteúdo técnico de hoje sai do README para `docs/`:

- `docs/instalar/escolha.md`: qual cenário é o seu, numa tabela de decisão (tem domínio? tem
  porta aberta? já tem proxy? só rede interna?).
- `docs/instalar/rede-local.md`, `vps.md`, `proxy-reverso.md`: passo a passo completo de cada
  um, do zero até a primeira máquina `em-dia`, com como conferir cada etapa.
- `docs/instalar/proxy-reverso.md` traz a receita do **Nginx** inteira (TLS com certbot,
  `proxy_pass`, `client_max_body_size`, tempos, cabeçalhos `X-Forwarded-*`, só HTTPS) e a
  **matriz de compatibilidade**, cada linha testada ou marcada como "não testado":

  | Proxy | Terminando TLS (`--behind-proxy`) | Repassando TLS intacto (passthrough) |
  |---|---|---|
  | Nginx | sim (receita completa) | sim, com `stream` + `ssl_preread` |
  | Nginx Proxy Manager | sim, proxy host comum | via "Streams", sem Let's Encrypt do NPM |
  | Caddy | sim (`compose.caddy.yml`) | só com plugin layer4 |
  | Traefik | sim, por labels | sim, `HostSNI` + `tls.passthrough` |
  | HAProxy / Apache | sim | HAProxy sim (modo tcp); Apache não |
  | Cloudflare Tunnel / proxy | sim, por causa da assinatura | não |

  Explicar a diferença em linguagem direta: terminando TLS, o proxy vê o conteúdo e o agente
  prova quem é pela assinatura; em passthrough o proxy não vê nada e vale o modo direto. Dizer
  o que **não** funciona: agente antigo (só mTLS) atrás de proxy que termina TLS; proxy que
  corta corpo grande ou reescreve o corpo.
- `docs/agentes.md` (instalar pelos três caminhos, atualizar, desinstalar, logs),
  `docs/operacao.md` (backup, restauração, atualização do servidor, monitoramento),
  `docs/problemas.md` (a tabela "Quando dá errado", ampliada com proxy e painel),
  `docs/seguranca.md` (quem confia em quem, o que o proxy enxerga, o código de configuração),
  `docs/api.md` (a tabela de rotas), `docs/desenvolvimento.md` (build, sqlc, testes).
- `docs/piloto.md` vira um roteiro curto que aponta para os documentos acima (mantém critérios
  de sucesso e o que anotar).
- Exemplos neutros no lugar de "Prefeitura" (ex.: "Minha Organização", `invenire.exemplo.org`).
  Idioma: pt-BR nesta rodada; estrutura de pastas que permita `docs/en/` depois.

**Pronto quando:** cada um dos três roteiros foi seguido do início ao fim numa máquina limpa,
só com o que está escrito; o do Nginx foi feito com um Nginx de verdade.

## Fase 5 — Abrir o código

- GitHub Actions: testes a cada push (com PostgreSQL de serviço); a cada tag, publicar
  binários do agente e do servidor com `SHA256SUMS` e a imagem multi-arquitetura no GHCR.
  `scripts/build.sh` continua valendo para build local.
- `CONTRIBUTING.md`, `SECURITY.md` (como relatar falha), `CHANGELOG.md`, modelos de issue
  (os campos de "O que anotar" do piloto: hostname, sistema, versão do agente, o que houve).
- Revisar o repositório atrás de nomes, endereços e segredos reais antes de publicar.
- `LICENSE`: AGPL-3.0 (decidido).

**Feito:** `LICENSE` (texto oficial da AGPL-3.0), SPDX `AGPL-3.0-only` nos `.go` (menos o código
gerado pelo sqlc); módulo `github.com/invenire-hq/invenire`; compose com a imagem
`ghcr.io/invenire-hq/invenire-server` e `build:` como alternativa; `.github/workflows/ci.yml` e
`release.yml` (actions fixadas por SHA, permissões por job, imagem multi-arquitetura por
compilação cruzada, sem QEMU); `CONTRIBUTING.md`, `CLA.md`, `SECURITY.md`, `CHANGELOG.md`
(0.1.0), modelos de issue e de pull request. Na revisão, a árvore perdeu o link para o documento
de arquitetura privado (claude.ai) e um nome real num teste; o histórico do git não foi
reescrito (achados no relatório da Fase 5: e-mail pessoal nos commits, o mesmo link privado,
exemplos antigos com "Prefeitura").

## Ordem e dependências

1 → 2 → 3 → 4 → 5. A Fase 1 é independente e destrava o teste na instituição mesmo sem painel.
A Fase 3 depende da 2 (tela do token) e é melhor com a 1 (comando de uma linha atrás de proxy).
A Fase 4 documenta o que as três primeiras entregam; os arquivos de `deploy/servidor/` podem
ser feitos junto com a Fase 1. Um commit (ou PR) por fase, com os testes da fase.

## Pendências para o Athos

1. ~~**Licença**~~ — **resolvida:** AGPL-3.0 (`LICENSE`, cabeçalho SPDX nos `.go`).
2. ~~**Onde o código vai ficar**~~ — **resolvida:** organização `invenire-hq`
   (`github.com/invenire-hq/invenire`, imagem `ghcr.io/invenire-hq/invenire-server`).
3. **Domínio do teste na instituição** e se o Nginx de lá já tem certificado público.
4. ~~**Confirmar a abordagem da Fase 1**~~ — **resolvida:** assinatura de requisição, mantida.
5. ~~**Cabeçalho SPDX**~~ — **resolvida:** `AGPL-3.0-only`.
6. ~~**Termo de contribuição**~~ — **resolvida:** CLA **e** DCO. O DCO (`git commit -s`) atesta a
   origem de cada commit, conferido no CI; o CLA (`CLA.md`), aceito uma vez no pull request, dá
   o direito de relicenciar. O texto do CLA merece a leitura de um advogado antes do primeiro
   pull request de fora.
7. ~~**Organização e repositório**~~ — **criados:** `https://github.com/invenire-hq/invenire`
   (público, vazio em 2026-11-23). Falta, no GitHub: ativar o relatório privado de
   vulnerabilidade (Settings > Security) e, depois da primeira tag, conferir se o pacote da
   imagem no GHCR ficou público.
8. **Histórico do git**, publicado como está (sem reescrita): o e-mail do autor aparece nos
   commits e há um link para um documento privado do claude.ai em commits antigos. A árvore
   atual não tem nenhum dos dois.

## Estado das fases

| Fase | Estado | Ainda não testado de verdade |
|---|---|---|
| 1 — Servidor atrás de proxy | Feita (`05d2858`) | — |
| 2 — Painel web | Feita (`b22e676`) | painel num navegador de verdade (testado por HTTP) |
| 3 — Instalador pré-configurado | Feita (`f906939`, `c75c620`) | Windows: segunda execução elevada do `Instalar.cmd`, comando de uma linha no PowerShell, SmartScreen |
| 4 — Implantação e documentação | Feita (`d7f34d3`) | certbot e Let's Encrypt reais; linhas da matriz de proxies além de Nginx e Caddy |
| 5 — Abrir o código | Feita na pasta de trabalho | workflows no GitHub de verdade (só validados localmente), publicação no GHCR, release |

## Futuro (registrado, fora deste plano)

- Janela gráfica no instalador do agente Windows (endereço + código de cadastro).
- Assinatura de código dos executáveis; pacote MSI para distribuição por GPO/Intune.
- Atualização automática do agente.
- Importação de tenant (o passo a passo está em `docs/exportacao.md`).
- Serviço hospedado: autocadastro, e-mail, limites por organização, cobrança, papel de
  operador da instalação no painel.
- Documentação em inglês.
