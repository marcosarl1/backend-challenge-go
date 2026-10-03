#!/usr/bin/env sh
# Confere no MiniStack que produtor e serviço só executam as ações permitidas.
# Rode com a aplicação parada e as filas de entrada vazias após make up.

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

as_producer_b() (
	. /credentials/sqs-producer-b.env
	export AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY
	aws --endpoint-url "$ENDPOINT" --region "$REGION" "$@"
)

MAIN_URL="$(as_service sqs get-queue-url --queue-name wager-transactions.fifo \
	--query QueueUrl --output text)"
PROVIDER_B_URL="$(as_service sqs get-queue-url --queue-name wager-transactions-provider-b.fifo \
	--query QueueUrl --output text)"
as_service sqs list-queues >/dev/null

expect_denied() {
	if DENIAL="$("$@" 2>&1)"; then
		echo "falha: acesso indevido permitido: $*" >&2
		exit 1
	fi
	case "$DENIAL" in
		*AccessDenied*) ;;
		*) echo "falha inesperada ao negar acesso: $DENIAL" >&2; exit 1 ;;
	esac
}

expect_denied as_producer sqs receive-message --queue-url "$MAIN_URL"
expect_denied as_producer_b sqs receive-message --queue-url "$PROVIDER_B_URL"
expect_denied as_producer sqs send-message --queue-url "$PROVIDER_B_URL" \
	--message-group-id auth-check --message-deduplication-id auth-check-denied-a \
	--message-body '{}'
expect_denied as_producer_b sqs send-message --queue-url "$MAIN_URL" \
	--message-group-id auth-check --message-deduplication-id auth-check-denied-b \
	--message-body '{}'
for url in "$MAIN_URL" "$PROVIDER_B_URL"; do
	expect_denied as_service sqs send-message --queue-url "$url" \
		--message-group-id auth-check --message-deduplication-id auth-check-denied \
		--message-body '{}'
done
if DENIAL="$(AWS_ACCESS_KEY_ID=unknown AWS_SECRET_ACCESS_KEY=unknown \
	aws --endpoint-url "$ENDPOINT" --region "$REGION" sqs list-queues 2>&1)"; then
	echo "falha: credencial desconhecida conseguiu listar filas" >&2
	exit 1
fi
case "$DENIAL" in
	*InvalidClientTokenId*|*UnrecognizedClientException*|*AccessDenied*) ;;
	*) echo "falha inesperada para credencial desconhecida: $DENIAL" >&2; exit 1 ;;
esac

check_delivery() {
	local producer="$1" url="$2" id="$3" received="" body="" receipt=""
	"$producer" sqs send-message --queue-url "$url" \
		--message-group-id auth-check --message-deduplication-id "$DEDUP_ID-$id" \
		--message-body "{\"authCheck\":\"$id\"}" >/dev/null
	received="$(as_service sqs receive-message --queue-url "$url" \
		--wait-time-seconds 1 --query 'Messages[0].[Body,ReceiptHandle]' --output text)"
	body="$(printf '%s\n' "$received" | cut -f 1)"
	receipt="$(printf '%s\n' "$received" | cut -f 2)"
	if [ "$body" != "{\"authCheck\":\"$id\"}" ] || [ -z "$receipt" ] || [ "$receipt" = None ]; then
		echo "falha: serviço não recebeu a mensagem do $id; confira se a fila estava vazia" >&2
		exit 1
	fi
	as_service sqs delete-message --queue-url "$url" --receipt-handle "$receipt" >/dev/null
}

check_delivery as_producer "$MAIN_URL" provider-a
check_delivery as_producer_b "$PROVIDER_B_URL" provider-b

echo "acesso SQS: cada provedor envia apenas à própria fila; serviço consome ambas"
