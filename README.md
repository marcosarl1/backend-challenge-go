# Desafio Backend — Processamento Distribuído de Apostas em Go

Serviço em Go para processar apostas e outros movimentos financeiros de
carteiras de jogadores em um ambiente distribuído.

A aplicação oferece uma API HTTP e um consumidor SQS. Os dois caminhos usam o
mesmo caso de uso e persistem o resultado no PostgreSQL. O projeto também
inclui autenticação OIDC, idempotência persistente, ledger append-only,
transactional outbox, workers recuperáveis e testes com processos e
dependências reais.

## Documentação

- [Arquitetura e decisões técnicas](ARCHITECTURE.md)
- [Contratos de rejeição e eventos](docs/CONTRACTS.md)
- [Exemplos de policies SQS para AWS](deploy/iam/README.md)
- [Enunciado do desafio](docs/DESAFIO.md)
- [Configuração do Keycloak](deploy/keycloak/README.md)

O README explica como executar o projeto. A arquitetura explica como as
garantias financeiras, a concorrência, a mensageria e o ciclo de vida
funcionam.

## Stack

| Área | Tecnologia |
| --- | --- |
| Linguagem | Go 1.27.1 |
| Composição | Uber Fx |
| API | `net/http` |
| Persistência | PostgreSQL com `pgx/v5` e SQL explícito |
| Mensageria | AWS SQS via MiniStack |
| Identidade | OAuth 2.0/OIDC via Keycloak |
| Observabilidade | Prometheus e OpenTelemetry |
| Ambiente local | Docker Compose |
| Testes de integração | Testcontainers |

## Pré-requisitos

- Go 1.27.1;
- Docker com Compose v2 e acesso ao daemon;
- `make`, `curl` e Python 3.

O Compose usa as portas `5432`, `4566` e `8080`. O serviço `app` publica
`8081` para HTTP e `9090` para métricas.

As credenciais do Compose são valores de teste para desenvolvimento local.
Não use essa configuração contra uma conta AWS real.

## Início rápido

Na raiz do repositório, suba o stack completo (infra + migrations + app):

```bash
cp .env.example .env
make up-full
```

Ou, sem o Compose na primeira subida:

```bash
docker compose up --build
```

O `up` cria as filas (`wager-transactions.fifo`,
`wager-transactions-provider-b.fifo`, `wager-transactions-dlq.fifo` e
`wager-events.fifo`), aplica as migrations e inicia o `app`. Ele também
ativa a autorização IAM do MiniStack e cria credenciais separadas para o
serviço e para cada provedor em `.local/`, fora do Git. `provider-a` envia
somente a `wager-transactions.fifo`, e `provider-b` somente a
`wager-transactions-provider-b.fifo`. O consumidor confere o `providerId` do
corpo contra a fila recebida antes da transação financeira.

Para desenvolvimento no host (binário fora do Compose), use o fluxo
alternativo — não rode junto com o `app` do Compose, pois ambos publicam
`8081`/`9090`:

```bash
cp .env.example .env
make up
make check-sqs-auth
make migrate-up
make run
```

Nesse fluxo, `make up` inicia somente PostgreSQL, MiniStack e Keycloak e
cria as filas. `make run` carrega apenas a credencial do serviço. Após
reiniciar o MiniStack, execute `make up` antes de iniciar a aplicação.

`make check-sqs-auth` envia e remove uma mensagem de prova na fila de entrada.
Ele verifica o isolamento entre provedores, o consumo pelo serviço e as
ações inversas negadas. Rode o comando antes de iniciar a aplicação, com as
filas de entrada vazias.

As migrations não rodam automaticamente quando a aplicação inicia fora do
Compose. Nesse fluxo, execute `make migrate-up` antes de iniciar o binário.
No stack completo (`docker compose up --build` ou `make up-full`), o serviço
`migrate` aplica as migrations antes do `app` iniciar.

Em outro terminal, confirme a prontidão:

```bash
curl -fsS http://localhost:8081/health/ready
```

O liveness do processo fica em `GET /health/live`. As métricas ficam
disponíveis em <http://127.0.0.1:9090/metrics>.

Para encerrar o ambiente:

```bash
make down
```

O comando encerra os containers e preserva o volume do PostgreSQL.

## Réplicas

O Compose sobe uma réplica do `app`. As garantias entre instâncias
independentes são demonstradas pelos testes, que iniciam três processos com
portas distintas sobre os mesmos containers:

```bash
go test -race -tags=integration -count=1 ./test/integration \
  -run '^TestMultiInstanceConcurrency$' -v
```

Escalar o serviço no Compose (`--scale app=N`) exige um override que
remova a publicação das portas `8081`/`9090` ou atribua um
`HTTP_ADDR`/`METRICS_ADDR` distinto por réplica, pois duas réplicas não
podem publicar a mesma porta do host.

## Problemas comuns

**`app` reiniciando com `UnrecognizedClientException` no SQS.** O MiniStack
não tem volume: recriar o container `sqs` apaga os usuários IAM, mas os
arquivos em `.local/` persistem no host e dessincronizam. O `sqs-init`
regenera as chaves ao detectar a divergência, e o `app` lê o arquivo atual
a cada (re)start. Se a rotação acontecer com o `app` já em execução (ex.:
`sqs-init` re-executado manualmente), ele mantém as variáveis antigas até
ser recriado. Recrie o `app` para absorver o arquivo atual:

```bash
docker compose up -d --force-recreate app
```

Em caso de dúvida, recrie tudo do zero (preserva o volume do banco):

```bash
docker compose down && make up-full
```

## Configuração

Use [.env.example](.env.example) como referência. O Compose lê `.env`; o
binário não carrega esse arquivo sozinho. `make run` carrega
`.local/sqs-service.env` para o cliente SQS. Os valores de exemplo coincidem
com os padrões do binário para execução no host.

O serviço `app` do Compose não usa `.env` para endereços: ele tem
hostnames internos fixos (`db`, `sqs`, `keycloak`) definidos no
`compose.yaml`. A única exceção é a credencial do serviço, lida de
`.local/sqs-service.env` via `env_file`. Por isso, `DATABASE_URL`,
`SQS_ENDPOINT` e `OIDC_JWKS_URL` com `localhost` valem para `make run`;
dentro do Compose valem os hostnames internos. `OIDC_ISSUER` permanece como
`http://localhost:8080/realms/wagering` nos dois fluxos, porque o `iss` do
token reflete o hostname usado na emissão.

As variáveis mais usadas são:

- `DATABASE_URL`: conexão com o PostgreSQL;
- `SQS_ENDPOINT`: endpoint do MiniStack;
- `OIDC_ISSUER`, `OIDC_JWKS_URL` e `OIDC_AUDIENCE`: validação dos tokens;
- `HTTP_ADDR`: listener da API;
- `METRICS_ADDR`: listener das métricas;
- `OTEL_EXPORTER_OTLP_ENDPOINT`: endpoint OTLP; vazio desativa traces;
- `SHUTDOWN_TIMEOUT`: prazo de encerramento do processo.

O binário usa a role `app`, com permissões limitadas. A role `wagering` fica
reservada às migrations; `make migrate-up` cria a role `app`.

`HTTP_ADDR` e `METRICS_ADDR` precisam usar portas diferentes. No Bash, altere
uma variável apenas para o processo iniciado:

```bash
HTTP_ADDR=:8082 make run
```

O migrador roda dentro da rede do Compose. Para informar a URL local de forma explícita:

```bash
make migrate-up \
  MIGRATE_DATABASE_URL='postgres://wagering:wagering@db:5432/wagering?sslmode=disable'
```

Para reverter uma migration:

```bash
make migrate-down
```

Cada execução reverte uma versão. Faça backup antes de usar esse comando em um
banco com dados que precisam ser preservados.

## Autenticação local

O realm `wagering` fornece três clientes `client_credentials`:

| Cliente | Permissão |
| --- | --- |
| `provider-a` | processa e consulta as próprias apostas |
| `provider-b` | processa e consulta as próprias apostas |
| `wallet-internal` | abre, consulta e reconcilia carteiras |

As credenciais estão em
[deploy/keycloak/README.md](deploy/keycloak/README.md). Gere os tokens no
mesmo Bash em que fará as chamadas:

```bash
TOKEN_URL=http://localhost:8080/realms/wagering/protocol/openid-connect/token

INTERNAL_TOKEN=$(curl -fsS "$TOKEN_URL" \
  --data-urlencode 'grant_type=client_credentials' \
  --data-urlencode 'client_id=wallet-internal' \
  --data-urlencode 'client_secret=wallet-internal-secret' \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')

PROVIDER_TOKEN=$(curl -fsS "$TOKEN_URL" \
  --data-urlencode 'grant_type=client_credentials' \
  --data-urlencode 'client_id=provider-a' \
  --data-urlencode 'client_secret=provider-a-secret' \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')
```

## Fluxo rápido da API

Abra uma carteira com o cliente interno:

```bash
PLAYER_ID=$(python3 -c 'import uuid; print(uuid.uuid4())')

WALLET_JSON=$(curl -fsS http://localhost:8081/wallets \
  --oauth2-bearer "$INTERNAL_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"playerId\":\"$PLAYER_ID\",\"initialBalance\":{\"amount\":\"100.00\",\"currency\":\"BRL\"}}")

printf '%s\n' "$WALLET_JSON"
WALLET_ID=$(printf '%s' "$WALLET_JSON" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
```

Registre uma aposta com o cliente do provedor:

```bash
BET_ID=bet-demo-$(python3 -c 'import uuid; print(uuid.uuid4())')

curl -fsS http://localhost:8081/wagering/transactions \
  --oauth2-bearer "$PROVIDER_TOKEN" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: provider-a:$BET_ID" \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"$BET_ID\",\"playerId\":\"$PLAYER_ID\",\"walletId\":\"$WALLET_ID\",\"roundId\":\"round-demo\",\"gameId\":\"game-demo\",\"kind\":\"BET\",\"money\":{\"amount\":\"25.00\",\"currency\":\"BRL\"}}"
```

A chamada retorna `PROCESSED` e deixa o saldo em `75.00`. Guarde o ID interno
para consultar depois. Repetir a mesma chamada `curl`, sem recriar `BET_ID`,
retorna `idempotentReplay: true`, sem criar outro débito.

```bash
BET_JSON=$(curl -fsS http://localhost:8081/wagering/transactions \
  --oauth2-bearer "$PROVIDER_TOKEN" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: provider-a:$BET_ID" \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"$BET_ID\",\"playerId\":\"$PLAYER_ID\",\"walletId\":\"$WALLET_ID\",\"roundId\":\"round-demo\",\"gameId\":\"game-demo\",\"kind\":\"BET\",\"money\":{\"amount\":\"25.00\",\"currency\":\"BRL\"}}")

printf '%s\n' "$BET_JSON"
BET_TX_ID=$(printf '%s' "$BET_JSON" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["transactionId"])')
```

### Crédito de prêmio (`WIN`)

Um `WIN` sem referência credita direto. Saldo: `75.00` → `85.00`.

```bash
WIN_ID=win-demo-$(python3 -c 'import uuid; print(uuid.uuid4())')

WIN_JSON=$(curl -fsS http://localhost:8081/wagering/transactions \
  --oauth2-bearer "$PROVIDER_TOKEN" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: provider-a:$WIN_ID" \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"$WIN_ID\",\"playerId\":\"$PLAYER_ID\",\"walletId\":\"$WALLET_ID\",\"roundId\":\"round-demo\",\"gameId\":\"game-demo\",\"kind\":\"WIN\",\"money\":{\"amount\":\"10.00\",\"currency\":\"BRL\"}}")

printf '%s\n' "$WIN_JSON"
WIN_TX_ID=$(printf '%s' "$WIN_JSON" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["transactionId"])')
```

### Aposta sem resultado (`LOSS`)

`LOSS` exige `amount` igual a `"0.00"`: não movimenta saldo, não cria
ledger nem altera a versão, mas gera `WagerTransactionProcessed`. Saldo
permanece `85.00`.

```bash
LOSS_ID=loss-demo-$(python3 -c 'import uuid; print(uuid.uuid4())')

curl -fsS http://localhost:8081/wagering/transactions \
  --oauth2-bearer "$PROVIDER_TOKEN" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: provider-a:$LOSS_ID" \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"$LOSS_ID\",\"playerId\":\"$PLAYER_ID\",\"walletId\":\"$WALLET_ID\",\"roundId\":\"round-demo\",\"gameId\":\"game-demo\",\"kind\":\"LOSS\",\"money\":{\"amount\":\"0.00\",\"currency\":\"BRL\"}}"
```

### Devolução (`REFUND`) e estorno (`ROLLBACK`)

`REFUND` devolve integralmente uma `BET` processada (crédito). `ROLLBACK`
desfaz uma `BET`, `WIN` ou `REFUND` com o movimento contrário (aqui, um
`ROLLBACK` do `WIN` debita `10.00`). Ambos exigem
`referenceExternalTransactionId` com mesmo provedor, jogador, carteira,
moeda, rodada e valor. Saldo: `85.00` → `110.00` → `100.00`.

```bash
REFUND_ID=refund-demo-$(python3 -c 'import uuid; print(uuid.uuid4())')

curl -fsS http://localhost:8081/wagering/transactions \
  --oauth2-bearer "$PROVIDER_TOKEN" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: provider-a:$REFUND_ID" \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"$REFUND_ID\",\"playerId\":\"$PLAYER_ID\",\"walletId\":\"$WALLET_ID\",\"roundId\":\"round-demo\",\"gameId\":\"game-demo\",\"kind\":\"REFUND\",\"money\":{\"amount\":\"25.00\",\"currency\":\"BRL\"},\"referenceExternalTransactionId\":\"$BET_ID\"}"

ROLLBACK_ID=rollback-demo-$(python3 -c 'import uuid; print(uuid.uuid4())')

curl -fsS http://localhost:8081/wagering/transactions \
  --oauth2-bearer "$PROVIDER_TOKEN" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: provider-a:$ROLLBACK_ID" \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"$ROLLBACK_ID\",\"playerId\":\"$PLAYER_ID\",\"walletId\":\"$WALLET_ID\",\"roundId\":\"round-demo\",\"gameId\":\"game-demo\",\"kind\":\"ROLLBACK\",\"money\":{\"amount\":\"10.00\",\"currency\":\"BRL\"},\"referenceExternalTransactionId\":\"$WIN_ID\"}"
```

Se a referência ainda não chegou, a resposta é `202` com `status`
`PENDING_REFERENCE`; um worker resolve ou expira a pendência depois.

### Rejeição por saldo (`422`)

Uma `BET` acima do saldo retorna `422` com `failureCode`
`INSUFFICIENT_FUNDS` e não movimenta nada. Com `-fsS` o `curl` esconde o
corpo (exit 22); use `-s` para inspecioná-lo:

```bash
BIGBET_ID=bet-demo-$(python3 -c 'import uuid; print(uuid.uuid4())')

curl -s http://localhost:8081/wagering/transactions \
  --oauth2-bearer "$PROVIDER_TOKEN" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: provider-a:$BIGBET_ID" \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"$BIGBET_ID\",\"playerId\":\"$PLAYER_ID\",\"walletId\":\"$WALLET_ID\",\"roundId\":\"round-demo\",\"gameId\":\"game-demo\",\"kind\":\"BET\",\"money\":{\"amount\":\"500.00\",\"currency\":\"BRL\"}}"
```

### Consultas de transação

Com o token do provedor dono da operação, por ID interno ou externo:

```bash
curl -fsS "http://localhost:8081/wagering/transactions/$BET_TX_ID" \
  --oauth2-bearer "$PROVIDER_TOKEN"

curl -fsS "http://localhost:8081/providers/provider-a/wagering/transactions/$BET_ID" \
  --oauth2-bearer "$PROVIDER_TOKEN"
```

Consultar transação de outro provedor retorna `404`, sem expor dados.

### Saldo, ledger e reconciliação

```bash
curl -fsS "http://localhost:8081/wallets/$WALLET_ID" \
  --oauth2-bearer "$INTERNAL_TOKEN"

curl -fsS "http://localhost:8081/wallets/$WALLET_ID/ledger?limit=50" \
  --oauth2-bearer "$INTERNAL_TOKEN"

curl -fsS -X POST "http://localhost:8081/wallets/$WALLET_ID/reconciliation" \
  --oauth2-bearer "$INTERNAL_TOKEN"
```

A reconciliação reconstrói o saldo do ledger (`100.00` de abertura `-25.00`
`+10.00` `+25.00` `-10.00` = `100.00`) e compara com o armazenado,
reportando `consistent` e `difference` sem alterar dados.

Valores monetários usam strings com duas casas decimais, como
`{"amount":"25.00","currency":"BRL"}`. Para testar outro fluxo, altere
`externalTransactionId` e `Idempotency-Key` juntos.

## Rotas principais

| Método e rota | Acesso | Uso |
| --- | --- | --- |
| `POST /wallets` | `internal` | Abre uma carteira |
| `GET /wallets/:walletId` | `internal` | Consulta saldo e versão |
| `GET /wallets/:walletId/ledger?cursor=...&limit=50` | `internal` | Lista o ledger |
| `POST /wallets/:walletId/reconciliation` | `internal` | Confere saldo e ledger |
| `POST /wagering/transactions` | `provider` | Processa uma operação |
| `GET /wagering/transactions/:transactionId` | `provider` autorizado | Consulta por ID interno |
| `GET /providers/:providerId/wagering/transactions/:externalTransactionId` | `provider` autorizado | Consulta por ID externo |
| `GET /health/live` | Público | Verifica se o processo está vivo |
| `GET /health/ready` | Público | Verifica PostgreSQL e SQS |

As rotas de negócio exigem um token Bearer. O header `Idempotency-Key` é
obrigatório em `POST /wagering/transactions`. Provedores só acessam os
próprios dados; operações de carteira exigem o cliente interno.

## Garantias principais

O serviço foi implementado para manter o resultado financeiro correto diante
de mensagens repetidas, concorrência entre instâncias, falhas entre commit e
publicação, referências fora de ordem, restart e indisponibilidade temporária.

As garantias dependem do PostgreSQL, da inbox/outbox e dos workers, não apenas
da deduplicação do SQS. [ARCHITECTURE.md](ARCHITECTURE.md) descreve as decisões
de implementação e os estados das transações. O catálogo de `failureCode`, os
eventos e as regras de consumo estão em [docs/CONTRACTS.md](docs/CONTRACTS.md).

## Testes

Os testes unitários não precisam do Compose. Os testes com a tag `integration`
criam PostgreSQL, Keycloak e MiniStack com Testcontainers, então o daemon
Docker precisa estar disponível.

```bash
make test
make test-race
make vet
make test-integration
```

Os testes cobrem parsing e operações de `Money`, invariantes da carteira,
transições de estado, idempotência, migrations, constraints, ledger
append-only, inbox, reentrega, retries, DLQ, outbox concorrente,
autenticação, autorização, isolamento entre provedores, concorrência entre
processos, restart, referências pendentes e ciclo de vida do Fx.

Para executar cenários específicos:

```bash
# Concorrência entre três processos.
go test -race -tags=integration -count=1 ./test/integration \
  -run '^TestMultiInstanceConcurrency$' -v

# Recuperação da outbox e disputa entre publishers.
go test -race -tags=integration -count=1 ./test/integration \
  -run 'Test(RecoveryAfterCommitBeforeDelete|RecoveryAfterCommitBeforePublish|RecoveryAfterPublishBeforeMark|TwoPublishersWithRealQueue)$' -v

# Restart, indisponibilidade temporária e ciclo de vida do Fx.
go test -race -tags=integration -count=1 ./test/integration \
  -run 'Test(RestartPreservesIdempotencyAndPending|TemporaryPostgresAndSQSOutage|FxModuleStartsAndStopsRealComponents)$' -v
```
