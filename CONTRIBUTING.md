# Como contribuir

Obrigado pelo interesse no Invenire. Relatos de problema, ideias, documentação e código são
bem-vindos. Este guia diz como montar o ambiente, testar e mandar uma mudança.

## Antes de começar

- **Problema ou ideia:** abra um issue com o modelo que melhor servir (relato de problema ou
  pedido de melhoria). Para mudanças grandes, converse no issue antes de escrever o código: evita
  trabalho que depois não entra.
- **Falha de segurança:** não abra issue público. Veja [SECURITY.md](SECURITY.md).
- **Idioma:** documentação, mensagens do programa, issues e commits são em português (pt-BR).

## Ambiente

Go (a versão de `go.mod`), Docker (para o PostgreSQL e o sqlc) e Git. O passo a passo completo,
com a estrutura das pastas, está em [docs/desenvolvimento.md](docs/desenvolvimento.md). O mínimo:

```sh
git clone https://github.com/invenire-hq/invenire.git
cd invenire
docker compose -f deploy/docker-compose.yml up -d --wait
export INVENIRE_TEST_DATABASE_URL="postgres://invenire:invenire@localhost:5442/invenire?sslmode=disable"
go test ./...
```

No PowerShell, troque `export X=…` por `$env:X="…"`.

## Antes de mandar

Rode o mesmo que a integração contínua (`.github/workflows/ci.yml`) roda a cada push e pull
request:

```sh
gofmt -l .                  # não pode listar nada
go vet ./...
GOOS=windows go vet ./...   # o agente tem código só de Windows
go test ./...               # com INVENIRE_TEST_DATABASE_URL definida
```

E, conforme o que mudou:

- **Banco** (`migrations/` ou `internal/server/store/queries/`): migração nova em vez de editar
  uma antiga, e o código do sqlc regenerado
  (`docker run --rm -v "$PWD:/src" -w /src sqlc/sqlc:1.30.0 generate`).
- **Formato do snapshot** (`pkg/snapshot`): `go generate ./pkg/snapshot`. O formato é contrato
  com agentes já instalados; mudança incompatível pede versão nova do formato.
- **Painel:** textos novos no catálogo (`internal/server/web/messages.go`); texto vindo das
  máquinas nunca vira `template.HTML`.
- **Comportamento visível** (comando, opção, rota, tela, instalação): atualize a documentação em
  `docs/` e acrescente uma linha em "Não lançado" no [CHANGELOG.md](CHANGELOG.md).
- **Arquivo `.go` novo:** comece com `// SPDX-License-Identifier: AGPL-3.0-only`, como os outros.

## Commits

- Em português, com o título dizendo **o que muda**, em até uns 72 caracteres, sem prefixo
  (`feat:`, `fix:`…) e sem ponto final. Exemplos do histórico:
  - `Agente só para quando o servidor recusa o crachá, não por qualquer 401`
  - `Rotas de consulta: ativos, software, histórico, instalações e mudanças`
- No corpo, quando ajudar, o **porquê** e o que foi testado.
- Um assunto por commit, com os testes dele. Commits de "conserta o anterior" podem ser juntados
  antes do pull request.
- Todo commit leva a linha `Signed-off-by` (`git commit -s`): é o DCO, explicado abaixo.

## Pull request

- Parta do `main` e mantenha a mudança focada.
- Descreva o que mudou, por quê e como testou (inclusive o que **não** deu para testar, como
  Windows, um proxy específico ou uma instalação real).
- A integração contínua precisa passar.

## Termo de contribuição: DCO e CLA

O Invenire é distribuído sob a [AGPL-3.0](LICENSE). O mantenedor pretende também oferecer o
Invenire como serviço hospedado e licenciar o código em outros termos a quem não puder aceitar a
AGPL (licença comercial). Para que isso continue possível com código de várias pessoas, toda
contribuição passa por dois compromissos, que se completam:

| | O que é | Quando | Como |
|---|---|---|---|
| **DCO** | o [Developer Certificate of Origin 1.1](https://developercertificate.org/): você atesta que o commit é seu, ou que tem o direito de enviá-lo, e que ele pode ser publicado no projeto | **em todo commit** | `git commit -s`, que acrescenta `Signed-off-by: Seu Nome <seu@email>` |
| **CLA** | o [termo de contribuição](CLA.md): você continua dono do que escreveu e dá ao projeto uma licença ampla para usar, modificar e **relicenciar** a contribuição, inclusive em termos comerciais | **uma vez**, no primeiro pull request | caixa marcada na descrição e o comentário abaixo |

O DCO registra a origem de cada mudança; o CLA dá ao projeto o direito de relicenciar, que o DCO
sozinho não dá. O CLA é curto: leia antes de mandar o primeiro pull request.

Como aceitar:

1. Assine cada commit com `git commit -s` (o nome e o e-mail do `Signed-off-by` são os do seu
   `git config`; use um nome real, não um apelido). A integração contínua confere a linha em
   todo commit de um pull request. Esqueceu? `git rebase --signoff main` e `git push --force`
   no seu branch.
2. No primeiro pull request, marque a caixa "Li e aceito o termo de contribuição (CLA.md)" na
   descrição e escreva, num comentário: `Aceito o termo de contribuição do Invenire (CLA.md).`

Se você contribui em nome de uma empresa ou do seu trabalho, confirme antes que pode aceitar o
termo por ela (muitos contratos de trabalho dão à empresa os direitos do código).

## Conduta

Trate todos com respeito, critique o código e não a pessoa, e lembre que muita gente aqui
contribui no tempo livre.
