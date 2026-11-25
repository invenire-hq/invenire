[Português](CONTRIBUTING.md) | English

# How to contribute

Thank you for your interest in Invenire. Bug reports, ideas, documentation and code are welcome.
This guide explains how to set up the environment, test and submit a change.

## Before you start

- **Bug or idea:** open an issue with the template that fits best (bug report or feature
  request). For large changes, discuss in the issue before writing the code: it avoids work that
  then does not get in.
- **Security issue:** do not open a public issue. See [SECURITY.en.md](SECURITY.en.md).
- **Language:** documentation, program messages, issues and commits are in Portuguese (pt-BR).
  The documentation also has an English version, in [`docs/en/`](docs/en/README.md).

## Environment

Go (the version in `go.mod`), Docker (for PostgreSQL and sqlc) and Git. The complete
walkthrough, with the folder layout, is in [docs/en/development.md](docs/en/development.md). The
minimum:

```sh
git clone https://github.com/invenire-hq/invenire.git
cd invenire
docker compose -f deploy/docker-compose.yml up -d --wait
export INVENIRE_TEST_DATABASE_URL="postgres://invenire:invenire@localhost:5442/invenire?sslmode=disable"
go test ./...
```

In PowerShell, replace `export X=…` with `$env:X="…"`.

## Before submitting

Run the same as continuous integration (`.github/workflows/ci.yml`) runs on every push and pull
request:

```sh
gofmt -l .                  # must list nothing
go vet ./...
GOOS=windows go vet ./...   # the agent has Windows-only code
go test ./...               # with INVENIRE_TEST_DATABASE_URL set
```

And, depending on what changed:

- **Database** (`migrations/` or `internal/server/store/queries/`): a new migration instead of
  editing an old one, and the sqlc code regenerated
  (`docker run --rm -v "$PWD:/src" -w /src sqlc/sqlc:1.30.0 generate`).
- **Snapshot format** (`pkg/snapshot`): `go generate ./pkg/snapshot`. The format is a contract
  with agents already installed; an incompatible change needs a new format version.
- **Dashboard:** new texts go in the catalog (`internal/server/web/messages.go`); text coming
  from the machines never becomes `template.HTML`.
- **Visible behavior** (command, option, route, screen, installation): update the documentation
  in `docs/` (and its English version in `docs/en/`, or say in the pull request that it still
  needs translating) and add a line under "Não lançado" (Unreleased) in
  [CHANGELOG.md](CHANGELOG.md).
- **New `.go` file:** start it with `// SPDX-License-Identifier: AGPL-3.0-only`, like the others.

## Commits

- In Portuguese, with the title saying **what changes**, in up to about 72 characters, with no
  prefix (`feat:`, `fix:`…) and no trailing period. Examples from the history:
  - `Agente só para quando o servidor recusa o crachá, não por qualquer 401`
    (Agent only stops when the server rejects the badge, not on any 401)
  - `Rotas de consulta: ativos, software, histórico, instalações e mudanças`
    (Query routes: assets, software, history, installations and changes)
- In the body, when it helps, the **why** and what was tested.
- One subject per commit, with its tests. "Fix the previous one" commits can be squashed before
  the pull request.
- Every commit carries the `Signed-off-by` line (`git commit -s`): that is the DCO, explained
  below.

## Pull request

- Branch from `main` and keep the change focused.
- Describe what changed, why and how you tested it (including what you could **not** test, such
  as Windows, a specific proxy or a real installation).
- Continuous integration must pass.

## Contributor agreement: DCO and CLA

Invenire is distributed under the [AGPL-3.0](LICENSE). The maintainer also intends to offer
Invenire as a hosted service and to license the code under other terms to those who cannot accept
the AGPL (commercial license). For that to remain possible with code from many people, every
contribution goes through two commitments, which complement each other:

| | What it is | When | How |
|---|---|---|---|
| **DCO** | the [Developer Certificate of Origin 1.1](https://developercertificate.org/): you certify that the commit is yours, or that you have the right to submit it, and that it may be published in the project | **on every commit** | `git commit -s`, which adds `Signed-off-by: Your Name <you@email>` |
| **CLA** | the [contributor agreement](CLA.md) (in Portuguese): you remain the owner of what you wrote and give the project a broad license to use, modify and **relicense** the contribution, including under commercial terms | **once**, on your first pull request | checkbox ticked in the description and the comment below |

The DCO records the origin of each change; the CLA gives the project the right to relicense, which
the DCO alone does not. The CLA is short: read it before submitting your first pull request.

How to accept:

1. Sign off each commit with `git commit -s` (the name and email in `Signed-off-by` are those from
   your `git config`; use a real name, not a nickname). Continuous integration checks the line on
   every commit of a pull request. Forgot? `git rebase --signoff main` and `git push --force` on
   your branch.
2. On your first pull request, tick the box "Li e aceito o termo de contribuição (CLA.md)" (I have
   read and accept the contributor agreement) in the description and write, in a comment:
   `Aceito o termo de contribuição do Invenire (CLA.md).` (I accept the Invenire contributor
   agreement.)

If you contribute on behalf of a company or as part of your job, first confirm that you may accept
the agreement on its behalf (many employment contracts give the company the rights to the code).

## Conduct

Treat everyone with respect, criticize the code and not the person, and remember that many people
here contribute in their spare time.
