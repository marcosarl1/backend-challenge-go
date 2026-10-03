#!/usr/bin/env sh
# Confere no MiniStack que produtor e serviço só executam as ações permitidas.
# Rode com a aplicação parada e a fila de entrada vazia após make up.

set -eu

ENDPOINT="${AWS_ENDPOINT_URL:-http://sqs:4566}"
REGION="${AWS_REGION:-us-east-1}"
DEDUP_ID="auth-check-$(date +%s)-$$"

as_service() (
	. /credentials/sqs-service.env
	export AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY
	aws --endpoint-url "$ENDPOINT" --region "$REGION" "$@"
)

as_producer() (
	. /credentials/sqs-producer.env
	export AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY
	aws --endpoint-url "$ENDPOINT" --region "$REGION" "$@"
)

MAIN_URL="$(as_service sqs get-queue-url --queue-name wager-transactions.fifo \
	--query QueueUrl --output text)"
as_service sqs list-queues >/dev/null

if DENIAL="$(as_producer sqs receive-message --queue-url "$MAIN_URL" 2>&1)"; then
	echo "falha: produtor conseguiu ler a fila de entrada" >&2
	exit 1
fi
case "$DENIAL" in
	*AccessDenied*) ;;
	*) echo "falha inesperada ao negar leitura do produtor: $DENIAL" >&2; exit 1 ;;
esac
if DENIAL="$(as_service sqs send-message --queue-url "$MAIN_URL" \
	--message-group-id auth-check --message-deduplication-id auth-check-denied \
	--message-body '{}' 2>&1)"; then
	echo "falha: serviço conseguiu enviar à fila de entrada" >&2
	exit 1
fi
case "$DENIAL" in
	*AccessDenied*) ;;
	*) echo "falha inesperada ao negar envio do serviço: $DENIAL" >&2; exit 1 ;;
esac
if DENIAL="$(AWS_ACCESS_KEY_ID=unknown AWS_SECRET_ACCESS_KEY=unknown \
	aws --endpoint-url "$ENDPOINT" --region "$REGION" sqs list-queues 2>&1)"; then
	echo "falha: credencial desconhecida conseguiu listar filas" >&2
	exit 1
fi
case "$DENIAL" in
	*InvalidClientTokenId*|*UnrecognizedClientException*|*AccessDenied*) ;;
	*) echo "falha inesperada para credencial desconhecida: $DENIAL" >&2; exit 1 ;;
esac

as_producer sqs send-message --queue-url "$MAIN_URL" \
	--message-group-id auth-check --message-deduplication-id "$DEDUP_ID" \
	--message-body '{"authCheck":true}' >/dev/null
RECEIVED="$(as_service sqs receive-message --queue-url "$MAIN_URL" \
	--wait-time-seconds 1 --query 'Messages[0].[Body,ReceiptHandle]' --output text)"
BODY="$(printf '%s\n' "$RECEIVED" | cut -f 1)"
RECEIPT="$(printf '%s\n' "$RECEIVED" | cut -f 2)"
if [ "$BODY" != '{"authCheck":true}' ] || [ -z "$RECEIPT" ] || [ "$RECEIPT" = None ]; then
	echo "falha: serviço não recebeu a mensagem do produtor; confira se a fila estava vazia" >&2
	exit 1
fi
as_service sqs delete-message --queue-url "$MAIN_URL" --receipt-handle "$RECEIPT" >/dev/null

echo "acesso SQS: produtor envia, serviço consome, ações cruzadas e chave desconhecida negadas"
