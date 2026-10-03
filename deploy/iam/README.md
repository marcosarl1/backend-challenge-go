# Acesso às filas em AWS

As policies IAM servem para o MiniStack local e como exemplos para AWS.
Substitua a conta `111111111111`, a região `us-east-1` e o role produtor da
conta `222222222222` pelos valores do ambiente antes de usar os arquivos na
AWS. No Compose, o init substitui conta e região ao criar usuários locais.

| Arquivo | Onde aplicar | Permissões |
| --- | --- | --- |
| [`producer.policy.json`](producer.policy.json) | Role do `provider-a` | Envia somente a `wager-transactions.fifo`. |
| [`producer-b.policy.json`](producer-b.policy.json) | Role do `provider-b` | Envia somente a `wager-transactions-provider-b.fifo`. |
| [`service.policy.json`](service.policy.json) | Role da aplicação, na conta das filas | Consome ambas as entradas, envia erros à DLQ e publica eventos. |
| [`event-consumer.policy.json`](event-consumer.policy.json) | Role do consumidor de eventos | Lê e confirma mensagens da fila de saída. |
| [`input-queue.policy.json`](input-queue.policy.json), [`input-b-queue.policy.json`](input-b-queue.policy.json) | `Policy` de cada fila em AWS | Exigem TLS e permitem envio pelos roles correspondentes de outra conta. |
| [`dlq-redrive-allow.json`](dlq-redrive-allow.json) | `RedriveAllowPolicy` da DLQ | Aceita ambas as filas como origem do redrive automático. |
| [`local-input-queue.policy.json`](local-input-queue.policy.json), [`local-input-b-queue.policy.json`](local-input-b-queue.policy.json) | `Policy` das filas locais | Negam leitura dos produtores, envio cruzado e envio pelo serviço. |

## Execução local

`make up` inicia o MiniStack com `AUTH=true`, cria as filas e aplica policies
aos usuários `wager-service`, `wager-producer` (`provider-a`) e
`wager-producer-b` (`provider-b`). As chaves ficam em
`.local/sqs-service.env`, `.local/sqs-producer.env` e
`.local/sqs-producer-b.env`, fora do Git. `make run` carrega só a chave do
serviço. Cada produtor envia apenas à própria fila; o serviço consome ambas,
mas não envia a elas.

Depois de `make up`, execute `make check-sqs-auth` com a aplicação parada e a
filas de entrada vazias. O comando envia, recebe e apaga uma mensagem de
prova em cada fila e verifica que as ações cruzadas falham.

O usuário administrador `test`/`test` só provisiona o emulador. Não use essa
identidade para iniciar a aplicação. O MiniStack ainda não verifica a
assinatura SigV4: a access key identifica o principal para a avaliação da
policy, mas não autentica criptograficamente o pedido. O Compose expõe a
porta 4566 só em `127.0.0.1`; o AWS SQS real valida a assinatura.

O serviço chama `ListQueues` no readiness; por isso a policy da aplicação
concede essa ação em `Resource: "*"`. As demais ações apontam para filas
específicas. O consumidor precisa de `ChangeMessageVisibility` para liberar
uma mensagem após falha ou desligamento. A aplicação envia mensagens inválidas
à DLQ com `SendMessage` e publica a outbox em `wager-events.fifo`. Os produtores
não recebem `ReceiveMessage` nem `DeleteMessage`; o consumidor de
eventos não recebe `SendMessage`.

Para acesso entre contas, configure tanto a policy do role produtor quanto a
policy da fila de entrada. Na mesma conta, as identity policies bastam para
os acessos descritos, desde que outras policies não concedam permissões mais
amplas. O `DenyPlainHTTP` explícito bloqueia chamadas sem TLS mesmo se outra
policy conceder acesso. Aplique a mesma regra de TLS às outras filas ao
implantá-las em AWS. A policy de redrive da DLQ controla quais filas podem
usá-la como destino; ela não substitui a policy de acesso da fila.

## Limite de confiança do produtor

Os provedores enviam diretamente ao SQS com identidades IAM distintas. A
permissão de cada role fica restrita à própria fila. O consumidor vincula a
fila recebida ao provedor esperado e manda à DLQ um envelope cujo
`data.providerId` diverge, antes de abrir a transação financeira. A inbox usa
nomes de consumidor distintos por provedor. O `SenderId` do MiniStack não
identifica o usuário IAM de forma confiável; por isso a autorização depende
das permissões das filas, não desse atributo.

As policies são exemplos para dois provedores. Para adicionar outro, crie
fila, role e policy próprios, configure o redrive e adicione um consumidor com
o vínculo de `providerId` correspondente. Não compartilhe access keys entre
provedores. Em AWS, exija TLS e use a identidade de execução do provedor;
as chaves estáticas são exclusivas do ambiente local.

O cliente SQS usa a cadeia padrão de credenciais do SDK, mas ainda exige
`SQS_ENDPOINT`. Para AWS, informe o endpoint regional e vincule as policies
a roles de execução; as credenciais geradas pelo MiniStack são apenas locais.
Se as filas usarem uma chave KMS gerenciada pelo cliente, configure também
as permissões KMS dos produtores e consumidores.

Referências: [ações IAM do SQS](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-api-permissions-reference.html),
[policies IAM e de fila](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-using-identity-based-policies.html),
[redrive allow policy](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-dead-letter-queues.html).
