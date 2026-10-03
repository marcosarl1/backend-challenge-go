# Contratos de rejeição e eventos

Este catálogo descreve os códigos persistidos em `wager_transactions` e os
eventos publicados em `wager-events.fifo`. O código-fonte define os valores em
`internal/domain/wager/kind.go` e os payloads em
`internal/domain/events/events.go`. Os arquivos `*.golden.json` em
`internal/domain/events/testdata/` mostram exemplos completos da versão 1.

## `failureCode`

Uma operação `REJECTED` guarda `failureCode` no banco, o devolve nas consultas
HTTP e gera `WagerTransactionRejected`. Repetir a operação preserva o código.
Uma operação `FAILED` guarda `INTERNAL_PERMANENT_FAILURE` para auditoria, mas
não gera evento de rejeição. Erros de formato, autorização, conflito de
idempotência e indisponibilidade transitória usam códigos de erro HTTP; não
entram neste catálogo.

| Código | Quando ocorre |
| --- | --- |
| `INSUFFICIENT_FUNDS` | `BET` excede o saldo disponível. |
| `REVERSAL_INSUFFICIENT_FUNDS` | `ROLLBACK` precisaria debitar além do saldo. |
| `REFERENCE_NOT_FOUND` | A espera por referência esgota 10 tentativas ou 10 minutos. |
| `REFERENCE_NOT_PROCESSED` | A referência termina em `REJECTED` ou `FAILED`. |
| `REFERENCE_MISMATCH` | Referência diverge em provedor, jogador, carteira, moeda ou rodada. |
| `REFERENCE_AMOUNT_MISMATCH` | Valor da operação difere do valor referenciado. |
| `INVALID_REFERENCE_KIND` | O tipo da referência não é permitido para esta operação. |
| `ALREADY_REVERSED` | A referência já recebeu uma reversão bem-sucedida. |
| `WALLET_MISMATCH` | Jogador informado não pertence à carteira. |
| `CURRENCY_MISMATCH` | Moeda informada difere da moeda da carteira. |
| `INTERNAL_PERMANENT_FAILURE` | Falha permanente de infraestrutura registrada como `FAILED`. |

`failureDetail` pode acompanhar uma rejeição para diagnóstico. Consumidores
devem tomar decisões pelo código, não pelo texto do detalhe.

## Envelope e destino

Todos os eventos usam a fila `wager-events.fifo` e o mesmo envelope JSON:

| Campo | Contrato |
| --- | --- |
| `eventId` | UUID estável; identifica o evento em republicações. |
| `eventType` | Um dos quatro tipos abaixo. |
| `aggregateId` | ID da transação para eventos `WagerTransaction*`; ID da carteira para `WalletBalanceChanged`. |
| `correlationId` | Identifica o fluxo que originou o evento. |
| `causationId` | ID da causa, quando informado; omitido quando vazio. |
| `occurredAt` | Instante UTC em RFC 3339. |
| `version` | Versão do contrato; atualmente `1`. |
| `data` | Objeto específico do `eventType`. |

O publicador envia o JSON do envelope como corpo da mensagem SQS. Ele usa
`walletId` como `MessageGroupId` e `eventId` como `MessageDeduplicationId`.
O atributo SQS `traceparent` pode acompanhar a mensagem para tracing; não
integra o contrato de negócio. O consumidor escolhe o tratamento por
`eventType` e `version`, sem depender de atributos do SQS para ler `data`.

## Payloads da versão 1

Valores monetários têm a forma `{"amount":"25.00","currency":"BRL"}`.
Campos UUID são strings. O JSON omite os campos marcados como opcionais
quando vazios.

| `eventType` | `aggregateId` | `data` | Emissão |
| --- | --- | --- | --- |
| `WagerTransactionProcessed` | Transação | `transactionId`, `walletId`, `playerId`, `kind`, `money`, `resultBalance`, `resultWalletVersion`; opcionais `providerId`, `externalTransactionId`, `roundId`, `gameId`. | Operação concluída, inclusive `LOSS` e `OPENING`. |
| `WagerTransactionRejected` | Transação | Campos de `WagerTransactionProcessed` mais `failureCode` e `failureDetail` opcional. | Rejeição definitiva de negócio. |
| `WalletBalanceChanged` | Carteira | `walletId`, `transactionId`, `direction` (`DEBIT` ou `CREDIT`), `money`, `balanceBefore`, `balanceAfter`, `walletVersion`. | Débito ou crédito efetivo, inclusive `OPENING` positivo; não sai para `LOSS` ou rejeição. |
| `WagerTransactionPendingReference` | Transação | `transactionId`, `providerId`, `externalTransactionId`, `walletId`, `referenceExternalTransactionId`, `attempts`, `nextAttemptAt`, `expiresAt`. | Primeiro registro de espera pela referência; retries não reemitem o aviso. |

`OPENING` não tem provedor, ID externo, rodada ou jogo; o evento processado
omite esses campos. `resultBalance` e `resultWalletVersion` representam o
resultado persistido da operação, inclusive nas rejeições. Os instantes
`nextAttemptAt` e `expiresAt` seguem o mesmo formato UTC de `occurredAt`.

## Consumo e entrega

O publisher só envia eventos depois do commit da transação financeira. Uma
queda após o envio e antes da confirmação da outbox pode repetir o mesmo
`eventId`. O consumidor deve persistir esse ID junto com seu efeito e
confirmar a mensagem depois do próprio commit. A deduplicação FIFO tem janela
limitada e não substitui esse registro durável.

O `MessageGroupId` reúne eventos da carteira na fila, mas a reserva de lotes
por publishers concorrentes não impõe ordem estrita de publicação dentro do
grupo. Quem precisa reconstruir o estado da carteira deve usar os campos de
versão e saldo do evento, tratar lacunas e não assumir que a ordem de chegada
equivale à ordem dos commits. O serviço não implementa um consumidor da fila
de saída; o sistema que a integrar define sua política de retry e DLQ.

Mudanças incompatíveis no formato de `data` exigem uma nova `version` e testes
de contrato. Um consumidor deve rejeitar ou estacionar versões desconhecidas
em vez de interpretá-las como versão 1.
