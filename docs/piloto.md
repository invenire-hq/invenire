# Roteiro do piloto

Até umas 20 máquinas, Windows e Linux, por duas semanas. O objetivo é descobrir o que quebra no
mundo real antes de ampliar: coletores, rede, relógio, antivírus, proxy, nomes de software que as
regras não conhecem. Este roteiro só diz a ordem; o como está nos documentos indicados.

## 0. Antes de começar

- [ ] Escolher o cenário do servidor ([escolha.md](instalar/escolha.md)) e ter o que o roteiro
      dele pede (máquina, endereço, domínio, portas).
- [ ] A lista das máquinas: hostname, sistema, setor e quem é o responsável. Misture Windows e
      Linux, desktop e servidor, e pelo menos uma máquina que fique desligada às vezes.
- [ ] Um lugar para os backups fora da máquina do servidor.
- [ ] Um teste do agente numa máquina sua antes de levar às máquinas dos outros.

## 1. Servidor

Siga o roteiro do cenário até a primeira máquina `em-dia`:
[rede local](instalar/rede-local.md), [VPS](instalar/vps.md) ou
[proxy reverso](instalar/proxy-reverso.md). Configure o backup diário no mesmo dia
([operacao.md](operacao.md#backup)).

## 2. Unidades e tokens

Em **Unidades**, crie a árvore que fizer sentido (por prédio, setor, sala). Em **Adicionar
máquinas**, um token por unidade deixa cada máquina já no lugar certo; para um lote, aumente
**Quantas máquinas** (por exemplo, 10) e use validade curta. Revogue o token quando o lote
terminar ou se o pacote se perder; as máquinas já cadastradas continuam.

## 3. Instalar nas máquinas

Pelo pacote do painel ou pelo comando de uma linha ([agentes.md](agentes.md#instalar)). O mesmo
pacote de um token de várias máquinas serve a todo o lote, por pendrive se for preciso; guarde-o
como uma senha e apague-o no fim.

## 4. Acompanhar

**Visão geral** no painel, todo dia, ou o `status` no cron
([operacao.md](operacao.md#monitorar)). O que cada estado quer dizer e o que fazer:
[problemas.md](problemas.md#estados-das-máquinas).

## 5. Critérios de sucesso

Ao fim de duas semanas:

- [ ] todas as máquinas `em-dia`, exceto as desligadas de propósito;
- [ ] nenhum snapshot com `erro`, ou cada um com issue aberto;
- [ ] nenhuma corrente quebrada sem explicação (relógio, restauração de imagem, clonagem);
- [ ] software instalado conferido à mão em três máquinas, uma de cada sistema, contra o que
      o Windows ou o gerenciador de pacotes mostra;
- [ ] os nomes sem produto mais comuns mapeados (à mão, em **Software**, ou por regra nova no
      catálogo);
- [ ] um backup restaurado de verdade ([operacao.md](operacao.md#restauração));
- [ ] uma atualização do agente feita pelo instalador em pelo menos duas máquinas
      ([agentes.md](agentes.md#atualizar));
- [ ] anotado o que os responsáveis das máquinas estranharam (CPU, antivírus, perguntas).

## 6. O que anotar

Cada surpresa vira issue com o hostname, o sistema, a versão do agente e o que aconteceu. As que
mais importam para depois: coletor que falha em algum sistema, nome de software que as regras
erram, máquina clonada (duas com o mesmo `smbios_uuid`), relógio errado, proxy ou antivírus que
bloqueia o agente, e o que confundiu no painel ou na instalação.
