# Roteiro do piloto

Até umas 20 máquinas, Windows e Linux, com o servidor numa máquina Linux com Docker. O
objetivo é descobrir o que quebra no mundo real antes de abrir o projeto e antes do primeiro
contrato: coletores, rede, relógio, antivírus, nomes de software que as regras não conhecem.

## 0. Antes de começar

- [ ] Uma máquina Linux para o servidor, com Docker e o plugin `compose`, que as máquinas do
      piloto alcancem por nome ou IP na porta escolhida (padrão 8443).
- [ ] O nome DNS e o IP pelos quais as máquinas vão chamar o servidor. Eles entram no
      certificado do servidor (`INVENIRE_HOSTS`); um endereço fora da lista é recusado pelo
      agente.
- [ ] A lista das máquinas: hostname, sistema, setor e quem é o responsável. Misture Windows e
      Linux, desktop e servidor, e pelo menos uma máquina que fique desligada às vezes.
- [ ] Um lugar para os backups fora da máquina do servidor.
- [ ] Um teste do agente como serviço do Windows numa máquina sua, com PowerShell como
      administrador (passo 4), antes de levar às máquinas dos outros.

## 1. Montar a versão

Numa máquina com Go e Git:

```sh
scripts/build.sh 0.1.0
```

Gera `dist/0.1.0/` com o agente para Windows e Linux (amd64 e arm64), o servidor, os scripts
de instalação e o `SHA256SUMS`. Copie a pasta inteira para um compartilhamento que as máquinas
do piloto leiam. Os scripts conferem o executável contra o `SHA256SUMS` antes de instalar.

## 2. Subir o servidor

Na máquina do servidor, com o repositório em `/opt/invenire`:

```sh
cd /opt/invenire/deploy/piloto
cp .env.example .env
openssl rand -hex 24          # cole em INVENIRE_PG_PASSWORD
vi .env                       # INVENIRE_HOSTS=invenire.prefeitura.local,10.0.0.5
docker compose up -d --build --wait
docker compose logs server | tail -3
```

A linha `invenire-server no ar` mostra `ca_sha256`: é o pin que os agentes usam para
reconhecer o servidor. O servidor e o PostgreSQL voltam sozinhos quando a máquina reinicia.

Os comandos de administração rodam dentro do contêiner. Para não repetir o prefixo:

```sh
alias inv='docker compose -f /opt/invenire/deploy/piloto/compose.yml exec server invenire-server'
```

## 3. Organização e token

```sh
inv tenant create "Prefeitura"
inv unit create --tenant "Prefeitura" --kind secretaria "Saúde"
inv unit create --tenant "Prefeitura" --parent "Saúde" --kind ubs "UBS Centro"
inv token create --tenant "Prefeitura" --unit "Saúde/UBS Centro" --uses 10 --ttl 72h --description "piloto, lote 1"
```

Um token com `--uses 10` cadastra até dez máquinas, todas na unidade dada. Anote o `id` e o
`ca pin`. Se o token vazar, corte na hora; as máquinas já cadastradas continuam:

```sh
inv token list --tenant "Prefeitura"
inv token revoke --tenant "Prefeitura" <id>
```

Um token por unidade deixa cada máquina já no lugar certo. Máquinas fora de unidade podem ser
postas depois pela API (`PUT /v1/assets/{id}/unit`).

## 4. Instalar nas máquinas

**Windows** (PowerShell **como administrador**, na pasta da versão):

```powershell
$env:INVENIRE_ENROLL_TOKEN = "inv_..."
.\instalar.ps1 -Server https://invenire.prefeitura.local:8443 -CaPin sha256:...
```

**Linux** (root, na pasta da versão):

```sh
sudo INVENIRE_ENROLL_TOKEN=inv_... ./instalar.sh --server https://invenire.prefeitura.local:8443 --ca-pin sha256:...
```

O script confere o executável, cadastra, instala o serviço e confirma que ele está rodando.
Rodar de novo não cadastra outra vez: só atualiza o serviço para o executável da pasta. É
assim que se atualiza o agente numa versão nova. `-Force` / `--force` cadastra de novo e
revoga o crachá anterior da máquina.

Num compartilhamento só leitura no Linux, copie a pasta para a máquina antes, se o executável
não estiver marcado como executável.

## 5. Acompanhar

```sh
inv status --tenant "Prefeitura"
```

Uma linha por máquina, as que precisam de atenção primeiro:

| Estado | Quer dizer | O que fazer |
|---|---|---|
| `em-dia` | o agente apareceu dentro do limite e tudo foi ingerido | nada |
| `na-fila` | snapshots esperando a ingestão | normal por alguns segundos; se persistir, veja os logs do servidor |
| `sem-coleta` | cadastrado, mas nenhum snapshot chegou | o serviço não subiu ou não alcança o servidor: veja o log do agente |
| `atrasado` | o agente não aparece há mais de 12h (`--stale`) | máquina desligada, sem rede, ou serviço parado |
| `erro` | algum snapshot não pôde ser ingerido | o motivo aparece no fim do `status`; abra um issue com ele |
| `sem-agente` | o agente foi revogado (ou a máquina recadastrada) | recadastre se a máquina ainda é do piloto |

O fim do `status` resume sistemas, versões do agente, nomes de software ainda sem produto e
quantos cadastros restam nos tokens. Para avisar sozinho, no cron:

```sh
0 8 * * * docker compose -f /opt/invenire/deploy/piloto/compose.yml exec -T server invenire-server status --tenant "Prefeitura" --check || <avisar>
```

Logs do agente: no Windows, Visualizador de Eventos → Aplicativo, origem `invenire-agent`; no
Linux, `journalctl -u invenire-agent`.

O inventário em si sai pela API (crie um usuário com `inv actor create --tenant "Prefeitura"
--name athos --scopes read,admin`): `GET /v1/assets`, `GET /v1/assets/{id}/software`,
`GET /v1/software`, `GET /v1/software/unmapped`.

## 6. Backup

```sh
cd /opt/invenire/deploy/piloto
./backup.sh /var/backups/invenire
```

Grava o dump do banco e os dados do servidor (CA da instalação e chave-mestra) numa pasta com
data e `SHA256SUMS`, e mantém os 14 mais novos. Um sem o outro não restaura. Ponha no cron
(`0 2 * * *`) e copie a pasta para fora da máquina. A senha do banco está no `.env`; guarde-a
também.

Para restaurar numa máquina nova, com o repositório e o `.env` no lugar:

```sh
./restaurar.sh /var/backups/invenire/20261110-161950
```

Restaurado com a mesma CA, o servidor é reconhecido pelos agentes sem nada mudar neles. Se o
endereço mudou, em cada máquina: `invenire-agent set-server --server https://novo:8443
--ca-pin sha256:...` e reinicie o serviço. O roteiro foi testado assim: instalação pelo script,
backup, restauração em outro projeto e `set-server` do agente para o restaurado.

Uma cópia portátil de um tenant, para guardar ou levar para outro servidor, é o
`inv tenant export` (veja [exportacao.md](exportacao.md)).

## 7. Desinstalar

Windows (PowerShell como administrador):
`& "$env:ProgramFiles\Invenire\invenire-agent.exe" service uninstall`. Linux:
`sudo /usr/local/bin/invenire-agent service uninstall`. O estado (`C:\ProgramData\Invenire` ou
`/var/lib/invenire`) fica; apague a pasta para sumir com o crachá. No servidor, revogue o
agente para ele parar de ser aceito.

## 8. Critérios de sucesso

Ao fim de duas semanas:

- [ ] todas as máquinas `em-dia` no `status`, exceto as desligadas de propósito;
- [ ] nenhum snapshot com `erro`, ou cada um com issue aberto;
- [ ] nenhuma corrente quebrada sem explicação (relógio, restauração de imagem, clonagem);
- [ ] software instalado conferido à mão em três máquinas, uma de cada sistema, contra o que
      o Windows ou o gerenciador de pacotes mostra;
- [ ] os nomes sem produto mais comuns mapeados (à mão ou por regra nova no catálogo);
- [ ] um backup restaurado de verdade;
- [ ] uma atualização do agente feita pelo script em pelo menos duas máquinas;
- [ ] anotado o que os responsáveis das máquinas estranharam (CPU, antivírus, perguntas).

## 9. O que anotar

Cada surpresa vira issue com o hostname, o sistema, a versão do agente e o que aconteceu. As
que mais importam para depois: coletor que falha em algum sistema, nome de software que as
regras erram, máquina clonada (duas com o mesmo `smbios_uuid`), relógio errado, proxy ou
antivírus que bloqueia o agente.
