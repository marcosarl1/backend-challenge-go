# Acesso às filas em AWS

As policies IAM servem para o MiniStack local e como exemplos para AWS.
Substitua a conta `111111111111`, a região `us-east-1` e o role produtor da
conta `222222222222` pelos valores do ambiente antes de usar os arquivos na
AWS. No Compose, o init substitui conta e região ao criar usuários locais.

| Arquivo | Onde aplicar | Permissões |
| --- | --- | --- |
| [`producer.policy.json`](producer.policy.json) | Role do produtor, na conta de origem | Envia operações à fila de entrada. Crie um role por produtor confiável. |
| [`service.policy.json`](service.policy.json) | Role da aplicação, na conta das filas | Consulta URLs e prontidão, consome a entrada, envia erros à DLQ e publica eventos. |
| [`event-consumer.policy.json`](event-consumer.policy.json) | Role do consumidor de eventos | Lê e confirma mensagens da fila de saída. |
| [`input-queue.policy.json`](input-queue.policy.json) | Atributo `Policy` da fila de entrada | Exige TLS e permite envio pelo role produtor de outra conta. |
| [`dlq-redrive-allow.json`](dlq-redrive-allow.json) | Atributo `RedriveAllowPolicy` da DLQ | Aceita a fila de entrada como origem do redrive automático. |
| [`local-input-queue.policy.json`](local-input-queue.policy.json) | `Policy` da fila local | Nega leitura ao produtor e envio ao serviço na fila de entrada. |

## Execução local

`make up` inicia o MiniStack com `AUTH=true`, cria as filas e aplica as
policies dos usuários `wager-service` e `wager-producer`. O init também aplica
`local-input-queue.policy.json` à fila de entrada. Ele gera duas access keys
em `.local/sqs-service.env` e `.local/sqs-producer.env`, com permissão de
leitura só para o usuário local. `make run` carrega a chave do serviço. O
produtor pode enviar à fila de entrada, mas não pode ler mensagens; o serviço
consome a entrada e publica na saída, mas não envia à entrada.

Depois de `make up`, execute `make check-sqs-auth` com a aplicação parada e a
fila de entrada vazia. O comando envia uma mensagem de prova, recebe e apaga
essa mensagem, e verifica que as ações cruzadas falham com as credenciais
limitadas.

O usuário administrador `test`/`test` só provisiona o emulador. Não use essa
identidade para iniciar a aplicação. O MiniStack ainda não verifica a
assinatura SigV4: a access key identifica o principal para a avaliação da
policy, mas não autentica criptograficamente o pedido. O Compose expõe a
porta 4566 só em `127.0.0.1`; o AWS SQS real valida a assinatura.

O serviço chama `ListQueues` no readiness; por isso a policy da aplicação
concede essa ação em `Resource: "*"`. As demais ações apontam para filas
específicas. O consumidor precisa de `ChangeMessageVisibility` para liberar
uma mensagem após falha ou desligamento. A aplicação envia mensagens inválidas
à DLQ com `SendMessage` e publica a outbox em `wager-events.fifo`. O produtor
externo não recebe `ReceiveMessage` nem `DeleteMessage`; o consumidor de
eventos não recebe `SendMessage`.

Para acesso entre contas, configure tanto a policy do role produtor quanto a
policy da fila de entrada. Na mesma conta, as identity policies bastam para
os acessos descritos, desde que outras policies não concedam permissões mais
amplas. O `DenyPlainHTTP` explícito bloqueia chamadas sem TLS mesmo se outra
policy conceder acesso. Aplique a mesma regra de TLS às outras filas ao
implantá-las em AWS. A policy de redrive da DLQ controla quais filas podem
usá-la como destino; ela não substitui a policy de acesso da fila.

Estas policies não verificam `providerId` dentro do corpo de uma mensagem.
O consumidor valida as regras financeiras, mas a fila compartilhada não
vincula o `providerId` ao role que enviou a mensagem. Produtores SQS devem
ser serviços confiáveis; para isolar provedores não confiáveis, use um
gateway que associe identidade ao payload ou filas separadas com validação
dessa associação antes do processamento. O exemplo não concede acesso a
produtores desconhecidos.

O cliente SQS usa a cadeia padrão de credenciais do SDK, mas ainda exige
`SQS_ENDPOINT`. Para AWS, informe o endpoint regional e vincule as policies
a roles de execução; as credenciais geradas pelo MiniStack são apenas locais.
Se as filas usarem uma chave KMS gerenciada pelo cliente, configure também
as permissões KMS dos produtores e consumidores.

Referências: [ações IAM do SQS](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-api-permissions-reference.html),
[policies IAM e de fila](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-using-identity-based-policies.html),
[redrive allow policy](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-dead-letter-queues.html).
