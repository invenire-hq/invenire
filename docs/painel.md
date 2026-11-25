Português | [English](en/dashboard.md)

# Painel web

O `invenire-server` serve um painel no navegador, na mesma porta da API: a API fica em `/v1`, o
painel em `/`. Não há nada a instalar à parte: HTML, estilo e o pouco de JavaScript vão dentro do
binário. Textos em português, num catálogo de mensagens (`internal/server/web/messages.go`) que
permite outro idioma depois.

## Primeira configuração

Numa instalação sem nenhuma organização, o servidor mostra no log uma linha
`primeira configuração` com um código (ou usa o de `INVENIRE_SETUP_CODE`):

```sh
docker compose logs server | grep "primeira configuração"
```

Abrir o endereço do servidor leva a `/setup`, que pede esse código, o nome da organização e o
primeiro administrador (nome, e-mail e senha). Sem o código certo, ou com uma organização já
criada, o `/setup` recusa. Em seguida o painel leva a **Adicionar máquinas**, que oferece criar a
primeira unidade e o primeiro token de cadastro. O passo a passo, por cenário, está nos
[roteiros de instalação](instalar/escolha.md).

A mesma organização com administrador também pode ser criada pela linha de comando:

```sh
invenire-server tenant create --admin-email ana@exemplo.org --admin-name "Ana" "Minha Organização"
```

(a senha vem de `INVENIRE_PASSWORD`, ou é gerada e mostrada).

## Telas

- **Visão geral**: o mesmo do `invenire-server status`, com o que fazer em cada estado
  ([problemas.md](problemas.md#estados-das-máquinas)). Atualiza sozinha a cada minuto.
- **Adicionar máquinas**: tokens de cadastro (criar, listar, revogar; o token aparece uma vez)
  e as três formas de instalar o agente ([agentes.md](agentes.md#instalar)).
- **Máquinas**: filtros (unidade, sistema, nome), software instalado, histórico, unidade,
  etiquetas, revogar o agente.
- **Unidades**: árvore, criar, renomear, mover, apagar.
- **Software**: produtos instalados e onde; não identificados, com a sugestão das regras e o
  mapeamento manual.
- **Acesso**: pessoas do painel (`read` ou `admin`) e tokens de API.
- **Exportar** a organização ([exportacao.md](exportacao.md)).
- **Minha conta**: trocar a senha. Quem tem mais de uma organização troca em **Organização**.

Toda ação passa pelas mesmas funções das rotas `/v1` e fica no `change_log` com quem fez.
Login, sessão, permissões e o código de configuração estão em [seguranca.md](seguranca.md#painel).

## Esqueci a senha

Quem tem acesso ao servidor troca:

```sh
docker compose exec server invenire-server user set-password --email ana@exemplo.org
```

A senha vem de `INVENIRE_PASSWORD`, ou é gerada e mostrada; as sessões abertas daquela pessoa
são encerradas.

## Modo direto e navegador

No modo direto (rede local), o navegador não conhece a CA da instalação e avisa que o certificado
não é confiável; e, como o servidor pede certificado cliente no TLS (por causa do mTLS dos
agentes), navegadores com certificado digital instalado (e-CPF, e-CNPJ) podem mostrar um seletor:
cancele. Atrás de proxy ou com o Caddy nada disso acontece; o proxy só precisa repassar o `Host`,
porque o painel monta com ele o endereço que vai nos pacotes do instalador.
