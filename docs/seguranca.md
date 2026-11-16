# Segurança

Quem confia em quem, o que cada peça enxerga e as escolhas de projeto por trás disso. Para
relatar uma falha, fale com quem mantém o projeto antes de publicá-la.

## Duas confianças separadas

| | Quem confere | Com o quê |
|---|---|---|
| O agente confere o servidor | o agente | a **CA da instalação** (`data/ca`), pelo pin que vai no pacote do instalador; ou, com certificado público (proxy, Caddy, `--tls-cert`), as CAs do sistema, como um navegador |
| O servidor confere o agente | o servidor | a **CA da organização** (tenant) do agente: uma por organização, independente, criada no primeiro cadastro |

A CA da instalação só existe no modo direto e emite, a cada subida, o certificado TLS do
servidor para os nomes de `INVENIRE_HOSTS` (`--hosts`). O pin é o SHA-256 do certificado dessa
CA: não é segredo, e é o mesmo para todas as máquinas.

A CA de cada organização fica no banco (`tenant_ca`), com a chave cifrada (AES-256-GCM) pela
**chave-mestra** (`data/master.key`, ou `INVENIRE_MASTER_KEY`). Sem a chave-mestra o servidor
ainda confere os crachás emitidos, mas não emite novos. Como a CA da organização não depende da
instalação, levar uma organização para outro servidor é levar a CA dela junto, e os agentes só
trocam de endereço. Revogar a CA de uma organização (`tenant_ca.revoked_at`) derruba todos os
crachás dela de uma vez; o cadastro seguinte cria uma CA nova.

Agentes cadastrados antes das CAs por organização têm crachá da CA da instalação. Na subida, o
servidor registra essa CA, só para conferir, em cada organização que tem esses agentes.

## O agente e o servidor

**Cadastro.** O token de cadastro (`inv_…`) é uma senha de uso limitado: autoriza uma máquina
(ou `N`, num lote) a se cadastrar até vencer. O banco guarda só o SHA-256 dele, e ele aparece uma
vez. Revogar um token não afeta as máquinas já cadastradas. O cadastro manda um CSR e recebe o
**crachá** (certificado cliente) assinado pela CA da organização. Cada máquina tem um agente:
cadastrar de novo a mesma máquina revoga o anterior. O identificador da máquina é declarado por
quem se cadastra; se alguém usar um token para se passar por outra máquina, o agente dela passa a
receber `401` em vez de os dois conviverem.

**Cada requisição.** O agente se identifica de dois jeitos ao mesmo tempo:

- **mTLS**: o crachá vai no TLS. Só funciona quando o TLS termina no próprio servidor (modo
  direto);
- **assinatura**: cabeçalhos `Invenire-Agent-Cert` (o crachá), `Invenire-Timestamp` e
  `Invenire-Signature`, uma assinatura com a chave do crachá sobre o método, o caminho da API, o
  timestamp e o SHA-256 do corpo **como enviado** (com gzip). Passa intacta por qualquer proxy
  HTTP.

O servidor confere a assinatura e depois o crachá exatamente como no mTLS: cadeia até a CA da
organização e revogação. No modo direto valem os dois, e o mTLS tem preferência quando o
certificado cliente vem no TLS; atrás de proxy (`--behind-proxy`), só a assinatura. O proxy não
tem a chave do crachá: ele não consegue se passar por um agente.

**Relógio.** O `Invenire-Timestamp` pode estar até **24 horas** longe do relógio do servidor,
para os dois lados: relógio errado é comum no parque. Se a máquina estiver mais longe que isso,
o agente acerta o timestamp pelo `Date` da resposta e tenta de novo uma vez.

**Repetição.** Não há nonce. Repetir um `POST /v1/snapshots` capturado dá `200` "repetido" e
não muda nada; um `GET /v1/agent` também não. Repetir um `POST /v1/agent/renew` derrubaria o
crachá que o agente já usa, então o servidor guarda o timestamp da última renovação assinada
(`agent.last_renew_signed_at`, migração `00011`) e só aceita outra com timestamp maior.

**Tamanho.** O limite (32 MiB no snapshot, como chega) é conferido antes de ler o corpo.

## O que o proxy enxerga

- **Terminando o TLS** (`compose.proxy.yml`, `compose.caddy.yml`): o proxy vê tudo o que passa
  em claro para ele: senhas e sessões do painel, tokens de cadastro, tokens de API e os
  inventários. Ele **não** consegue se passar por um agente cadastrado (não tem a chave do
  crachá), mas, como vê os tokens de cadastro, poderia cadastrar uma máquina falsa com um token
  ainda válido. Use um proxy que você controla.
- **Passthrough**: o proxy vê só o nome pedido (SNI) e os bytes cifrados.

**`--trusted-proxies`.** Atrás de proxy, o servidor só lê `X-Forwarded-For` e
`X-Forwarded-Proto` de conexões vindas das faixas listadas. De qualquer outra, uma requisição com
`X-Forwarded-*` ou `Forwarded` recebe `403`, para ninguém forjar o próprio endereço. O endereço
do cliente é o mais à direita no `X-Forwarded-For` que não seja de um proxy confiável. Um
`X-Forwarded-Proto` diferente de `https` também recebe `403`: senha ou token teriam passado em
claro. No `compose.proxy.yml` a faixa padrão é a rede do compose, porque o proxy na mesma
máquina chega por ela; isso quer dizer que **qualquer processo da máquina** que alcance a porta
`127.0.0.1:8080` é tratado como proxy.

## Painel

- **Primeira configuração.** Numa instalação sem organização, o `/setup` exige o **código de
  configuração**, mostrado uma vez no log do servidor (ou dado em `INVENIRE_SETUP_CODE`): quem
  achar primeiro uma instalação nova exposta na internet não toma posse dela. O código tem cerca
  de 60 bits, vale até a primeira organização ser criada, e cada subida sem organização gera
  outro. Faça a configuração logo depois de subir.
- **Login.** E-mail (único na instalação) e senha guardada com **argon2id**. Sessão em cookie
  `__Host-invenire-sessao` (`HttpOnly`, `Secure`, `SameSite=Lax`); o banco guarda só o hash do
  identificador. A sessão cai depois de 12 horas sem uso e de 7 dias em qualquer caso. Depois de
  10 senhas erradas (por e-mail e por endereço), o login trava por 15 minutos.
- **Formulários.** Token CSRF em toda tela logada, e recusa de envio vindo de outra origem
  (`Sec-Fetch-Site`/`Origin`) também no login e no setup.
- **Permissões.** Os escopos do ator valem no painel como na API: `read` só consulta; `admin`
  também altera. A organização vem sempre da sessão, nunca da URL ou do formulário. Toda ação
  passa pelas mesmas funções das rotas `/v1` e fica no `change_log` com quem fez.
- **Cabeçalhos.** `Content-Security-Policy` só da própria origem, sem moldura
  (`frame-ancestors 'none'`), `nosniff`, e páginas fora do cache.
- **Recuperação de acesso** é pela linha de comando do servidor (`user set-password`): quem tem
  acesso ao servidor recupera a conta. Não há envio de e-mail.

O painel não separa "administrador da organização" de "operador da instalação": quem cuida da
instalação é quem tem a linha de comando do servidor.

## Pacote do instalador

O pacote baixado do painel **contém o token de cadastro** (no `instalar.conf`). O servidor não
guarda o token para montá-lo: o botão de baixar é um formulário `POST` que devolve ao servidor o
token que a página está mostrando, com a sessão de administrador e o CSRF; o servidor confere
pelo hash que o token é da organização da sessão e ainda vale, monta o pacote em memória e o
entrega. Cada pacote baixado fica no `change_log` (`enrollment_package_downloaded`).

Trate o pacote como uma senha: prazo curto, poucos usos, revogar se vazar. Depois de instalar, o
instalador apaga o `instalar.conf` se o token era de uma máquina só.

Os executáveis do agente são públicos (`/download/`): não são segredo. Os instaladores os
conferem contra o `SHA256SUMS` antes de rodar. Esse `SHA256SUMS` vem do mesmo servidor, então a
confiança está no canal: o pin (dentro do pacote) ou o certificado público. Por isso, com pin, o
comando de uma linha não existe: o download não teria como conferir o servidor. Os executáveis
ainda não têm assinatura de código.

## Texto vindo das máquinas

Quem controla uma máquina inventariada controla o texto que ela manda: hostname, nomes de
software, fabricante, versão, caminho. O servidor o grava sem caracteres de controle nem de
formatação invisíveis e truncado; o painel o escapa sempre (nunca vira HTML); a API o entrega
dentro de `collected`. É dado, não instrução, inclusive para agentes de IA que leiam a API. Um
snapshot com `\u0000` é recusado com `422`.

## Dados guardados

Cada snapshot é guardado duas vezes: `payload` (jsonb, para consultar) e `body` (os bytes exatos
que o agente enviou, comprimidos com lz4). Um `CHECK` no banco garante `sha256(body) = sha256`,
e cada snapshot aponta o anterior (`prev_sha256`): dá para reconferir a corrente de uma máquina
só com o que está no banco, e a exportação leva os bytes originais.
