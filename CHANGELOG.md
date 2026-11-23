# Mudanças

As mudanças de cada versão do Invenire. O formato segue
[Keep a Changelog](https://keepachangelog.com/pt-BR/1.1.0/), e as versões seguem
[Versionamento Semântico](https://semver.org/lang/pt-BR/). Enquanto a versão for `0.x`, uma
versão nova pode mudar comportamento; as mudanças que pedem ação na atualização aparecem aqui.

A publicação de uma versão (tag `vX.Y.Z`) tira as notas da release da seção desta página com o
mesmo número.

## [Não lançado]

## [0.1.0]

Primeira versão pública.

### Agente (`invenire-agent`)

- Coleta de hardware (SMBIOS), sistema operacional e software instalado no Windows e no Linux,
  num snapshot com formato versionado (`pkg/snapshot`, JSON Schema em `schema/v1.json`).
- Cadastro com token de uso limitado; o servidor é conferido pelo pin da CA da instalação ou por
  certificado público.
- Serviço do Windows (com Event Log) e unit do systemd; fila local que guarda os snapshots
  enquanto o servidor não responde.
- Autenticação por certificado de cliente (mTLS) e por assinatura de cada requisição, o que
  permite o servidor atrás de um proxy reverso que termina o TLS.
- Renovação automática do crachá; recadastro de uma máquina revoga o crachá anterior.
- Só para de enviar quando o servidor recusa o crachá, não por qualquer resposta 401.

### Servidor (`invenire-server`)

- API em `/v1` com PostgreSQL: cadastro, recebimento de snapshots (com os bytes originais),
  consultas de ativos, software, histórico, instalações e mudanças.
- Várias organizações (tenants) por servidor, cada uma com a própria CA; unidades em árvore,
  etiquetas e tokens de cadastro ligados a uma unidade e com número de usos.
- Ingestão com diferença de instalações e remoções; recusa de snapshot com data no futuro.
- Normalização de software por catálogo comum e regras, lista de não identificados e
  mapeamento manual por organização.
- Exportação completa da organização (formato 1), com `tenant export`, `verify` e
  `GET /v1/export`.
- Modo `--behind-proxy` (HTTP atrás de proxy, com `--trusted-proxies`).
- Comando `status` (com `--check` para monitoramento) e linha de comando de administração.

### Painel web

- Embutido no servidor, na mesma porta da API: primeira configuração protegida por código,
  login com senha (argon2id), sessões, proteção CSRF e limite de tentativas.
- Visão geral, adicionar máquinas (tokens), máquinas, unidades, software, acesso (usuários e
  tokens de API) e exportação; usuários `read` não alteram nada.

### Instalação

- O servidor distribui o agente em `/download/` (Windows amd64, Linux amd64 e arm64, com
  `SHA256SUMS`) e monta, pelo painel, um pacote pré-configurado do instalador (`.zip` com
  `Instalar.cmd` para Windows, `.tar.gz` para Linux) e o comando de uma linha.
- Implantação com Docker Compose em três cenários (`deploy/servidor/`): rede local, VPS com
  Caddy e Let's Encrypt, e atrás de um proxy que já existe (exemplo de Nginx); backup e
  restauração.
- Documentação de instalação, operação, segurança, API e desenvolvimento em `docs/`.

### Projeto

- Código aberto sob a AGPL-3.0, termo de contribuição (CLA), política de segurança e modelos de
  issue.
- Integração contínua (gofmt, go vet, sqlc, testes com PostgreSQL, imagem Docker) e publicação
  automática a cada tag: executáveis com `SHA256SUMS` na release e imagem
  `ghcr.io/invenire-hq/invenire-server` para amd64 e arm64.

[Não lançado]: https://github.com/invenire-hq/invenire/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/invenire-hq/invenire/releases/tag/v0.1.0
