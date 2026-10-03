# Arquitetura — Processamento de Apostas

## Visão geral

A API HTTP e o consumidor SQS chamam o mesmo caso de uso. O PostgreSQL guarda
o estado financeiro; o SQS entrega operações e eventos com semântica
*at-least-once*. Inbox, chaves únicas e outbox mantêm o resultado correto após
reentregas e reinícios. O domínio não depende de HTTP, SQS, pgx ou Fx.

## Dinheiro, carteira e ledger

`Money` guarda centavos em `int64` e o código da moeda. O contrato externo
aceita strings decimais com duas casas; rejeita sinal negativo, notação
científica, escala diferente, moeda sem duas casas e overflow. Soma,
subtração e negação também verificam overflow; operações entre moedas
diferentes falham. O PostgreSQL persiste os centavos em `BIGINT`, sem
conversão por ponto flutuante. Os cenários principais usam BRL.

Há uma carteira por `(playerId, currency)`. Ela começa na versão 1; cada
movimento posterior incrementa a versão. Saldo inicial positivo cria uma
transação interna `OPENING` e um crédito no ledger, sem incrementar a versão;
saldo inicial zero não cria movimento. Cada débito ou crédito grava saldo e
lançamento na mesma transação SQL. A constraint `CHECK` impede saldo negativo,
e a unicidade `(wallet, transaction)` impede lançamento duplicado. Triggers
barram `UPDATE`, `DELETE` e `TRUNCATE` do ledger, inclusive para o dono das
tabelas; a role da aplicação só pode inserir e ler. Uma correção financeira
exige outro lançamento. A reconciliação compara saldo e soma do ledger em uma
visão consistente, relata divergências e não altera dados.

## Transação SQL, concorrência e idempotência

O acesso ao banco usa `pgx/v5` e SQL explícito. A unidade de trabalho entrega
a mesma transação aos repositórios de carteira, operação, ledger, inbox e
outbox. O caminho normal processa e confirma a operação em um commit. A
aplicação bloqueia a linha da carteira com `FOR UPDATE` e atualiza o saldo
conferindo a versão anterior. Assim, operações da mesma carteira se
serializam no banco; carteiras diferentes avançam em paralelo. O runner
repete conflitos transitórios de escrita com limite.

Para operações externas, `(providerId, idempotencyKey)` e
`(providerId, externalTransactionId)` são únicos no banco. O hash SHA-256
cobre os campos de negócio em JSON canônico com chaves ordenadas:
`providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`,
`gameId`, `kind`, `money` e referência, quando presente. Chave de
idempotência e metadados de transporte ficam fora do hash. A entrada
monetária já exige formato canônico; referência ausente e `null` são
equivalentes. HTTP e SQS usam a mesma função de hash. Repetir chave e
conteúdo devolve o resultado persistido, inclusive o saldo observado na
época; reutilizar a chave com outro conteúdo retorna `409`. Trocar a chave
sem trocar o ID externo também conflita.

## Estados, referências e reversões

Operações externas começam em `PENDING` e terminam em `PROCESSED`,
`REJECTED` ou `FAILED`; `PENDING_REFERENCE` registra espera por outra
operação. Estados terminais são imutáveis. `FAILED` fica reservado a falha
permanente de infraestrutura; regras de negócio produzem `REJECTED` com
`failureCode` persistido. `OPENING` é interno e nasce processado.

`BET` debita; `WIN` credita; `LOSS` não movimenta saldo nem versão. `REFUND`
devolve uma `BET` processada. `ROLLBACK` inverte uma `BET`, `WIN` ou
`REFUND` processada. A referência deve coincidir em provedor, jogador,
carteira, moeda, rodada e valor. Uma operação-alvo admite no máximo uma
reversão bem-sucedida, seja `REFUND` ou `ROLLBACK`; para desfazer um
`REFUND`, o `ROLLBACK` aponta para ele. Se a inversão exigir débito sem
saldo, a aplicação rejeita com código distinto do usado por `BET`.

`REFUND`, `ROLLBACK` e `WIN` referenciado aguardam em `PENDING_REFERENCE`
quando a referência não chegou ou ainda não terminou. Um worker consulta as
pendências no banco e tenta novamente com backoff de 1 a 60 segundos. Após
10 tentativas ou 10 minutos, rejeita com `REFERENCE_NOT_FOUND`. Referência
terminada sem sucesso gera rejeição própria. A espera persiste no banco;
outra instância a retoma depois de uma queda. O catálogo de códigos está em
[docs/CONTRACTS.md](docs/CONTRACTS.md).

## Inbox, SQS e outbox

O consumidor usa o `messageId` do envelope como identidade durável e
confere o SHA-256 versionado dos bytes do envelope nas reentregas. Esse hash
inclui a chave de idempotência e os metadados, diferentemente do hash
financeiro compartilhado por HTTP e SQS. Hashes antigos na inbox não são
considerados equivalentes: uma reentrega correspondente vai à DLQ, sem
reexecutar a operação. A inbox e o resultado financeiro entram na
mesma transação SQL. Ele apaga a mensagem do SQS só depois do commit;
rejeição de negócio confirmada e referência pendente persistida também
permitem o delete. Erros transitórios mantêm a mensagem para nova tentativa;
mensagens inválidas vão à DLQ. Cada provedor tem uma fila de entrada FIFO;
ambas agrupam mensagens por carteira e
usa `messageId` para deduplicação. O init local configura visibilidade de
30 segundos e redrive após cinco recebimentos. Essas funções do SQS ajudam
na entrega; as garantias financeiras dependem do PostgreSQL.

A aplicação grava eventos de operação processada, rejeitada, saldo alterado
e referência pendente na outbox, junto com o estado que os originou. Um
publisher reserva lotes com `SKIP LOCKED` e lease, publica fora da transação
SQL e marca cada evento como enviado. Se cair entre o envio e a marcação,
outra instância pode publicar o mesmo `eventId`; consumidores da fila de
saída devem deduplicar por esse ID. Falhas de envio são reagendadas com
backoff e jitter. O publisher usa a carteira como grupo FIFO e o ID do
evento como deduplicação. Os contratos e as regras de consumo estão em
[docs/CONTRACTS.md](docs/CONTRACTS.md); os testes mantêm golden files dos
payloads.

## Identidade e autorização

O Keycloak foi escolhido como IdP externo porque pode rodar no Compose sem
que a aplicação cadastre senhas ou emita tokens. No ambiente local, fornece
tokens OAuth 2.0/OIDC pelo fluxo `client_credentials` para `provider-a`,
`provider-b` e `wallet-internal`. O fluxo atende à comunicação serviço-a-serviço
do desafio. A API valida assinatura RS256 via JWKS, emissor, audiência,
validade e role.
O claim `provider_id` do token define o provedor autorizado: divergência no
corpo ou caminho retorna `403`, e a consulta de transação alheia retorna
`404`. A role `provider` só processa e consulta transações do seu provedor;
a role `internal` restringe operações de carteira e reconciliação ao serviço
interno.

Foi escolhido o isolamento por fila para o envio direto dos provedores ao
SQS. `provider-a` envia a `wager-transactions.fifo`; `provider-b` envia a
`wager-transactions-provider-b.fifo`. As policies IAM restringem cada
identidade à própria fila, e o consumidor compara o `providerId` do envelope
com o provedor vinculado à fila antes de executar o caso de uso. Divergências
vão à DLQ sem transação financeira. O MiniStack local avalia policies IAM e
de fila com `AUTH=true`; o init cria credenciais distintas para serviço e
provedores. A aplicação usa a cadeia de credenciais do SDK. O emulador
identifica a access key, mas não verifica a assinatura SigV4. Os exemplos e
os requisitos de implantação estão em [deploy/iam/README.md](deploy/iam/README.md).

## Fx, desligamento e observabilidade

`platform.Module` reúne os módulos de configuração, observabilidade,
PostgreSQL, SQS, autenticação, repositórios, aplicação, HTTP e workers. O
`fx.Lifecycle` verifica as dependências antes de aceitar tráfego. No
desligamento, o HTTP para de receber requisições; workers param e aguardam
o trabalho em curso; o consumidor libera a visibilidade de mensagens não
confirmadas para reentrega. O pool fecha depois de seus consumidores. O
prazo vem de `SHUTDOWN_TIMEOUT`.

Logs JSON com `slog` incluem identificadores de correlação disponíveis,
sem tokens nem payload financeiro completo. Prometheus expõe métricas por
processo em `METRICS_ADDR`, separado da API. `/health/live` indica processo
vivo; `/health/ready` verifica PostgreSQL e SQS. Traces OpenTelemetry
propagam `traceparent` da entrada HTTP até a outbox e a mensagem SQS; a
exportação OTLP é opcional.

## Ambiente, testes e limites

O Compose inicia PostgreSQL, MiniStack, Keycloak, a criação das filas
(`sqs-init`), as migrations (`migrate`) e o serviço `app`, nessa ordem via
`depends_on` (`service_healthy` para infra, `service_completed_successfully`
para os jobs curtos). `docker compose up --build` (ou `make up-full`) sobe
o stack completo sem passos manuais; para desenvolvimento com o binário no
host, `make up` sobe só a infra e `make run` inicia a aplicação. Os dois
fluxos não podem rodar ao mesmo tempo, pois ambos publicam `8081`/`9090`.

Dentro da rede do Compose, o `app` usa hostnames internos (`db`, `sqs`,
`keycloak`); só `OIDC_ISSUER` permanece como `http://localhost:8080/...`,
porque o `iss` do token reflete o hostname usado na emissão, enquanto a
busca do JWKS usa `OIDC_JWKS_URL` interno. A credencial do serviço vem do
arquivo gerado pelo `sqs-init` (`.local/sqs-service.env`, via `env_file`
opcional). O Compose preserva o volume do banco ao parar e usa credenciais
locais de teste. O README traz os comandos completos.
O processo usa a role PostgreSQL `app`, com permissões limitadas; a role
`wagering` é usada apenas para migrations.

Testes unitários cobrem domínio e adaptadores. A suíte de integração usa
Testcontainers com PostgreSQL, Keycloak e MiniStack reais, além de processos
independentes para concorrência, recuperação e desligamento. Execute-a com
`make test-integration` e Docker disponível.

Limites e trabalho não concluído:

- O FIFO só deduplica dentro de sua janela; inbox e constraints do banco
  sustentam a idempotência durável.
- As métricas são locais a cada réplica e reiniciam com o processo.
- O Compose sobe uma réplica do `app` por padrão; escalar exige override de
  portas/`HTTP_ADDR` distintos. A prova com três instâncias independentes
  está nos testes multi-instância, não no Compose.
- O cliente SQS ainda exige um endpoint configurado. Uma implantação em AWS
  precisa apontá-lo para a região correta e vincular roles às instâncias.

## Interpretações adotadas

- `WIN` com referência ausente segue a espera das reversões.
- Uma operação aceita uma reversão bem-sucedida; `ROLLBACK` de `REFUND`
  referencia o reembolso.
- Carteira inexistente retorna não encontrado sem persistir a operação;
  jogador ou moeda divergentes geram rejeição auditável.
- Replay de pendência preserva o estado pendente; replay de rejeição
  preserva o código original.
- Falhas transitórias não tornam uma operação `FAILED`.
