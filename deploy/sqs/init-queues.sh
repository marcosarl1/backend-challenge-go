#!/usr/bin/env sh
# Cria as filas SQS usadas pelo serviço de apostas no emulador MiniStack.
# Pode rodar quantas vezes quiser: filas que já existem são mantidas e os
# atributos da fila principal são ajustados para o valor esperado.
#
# Filas:
#   wager-transactions.fifo      (entrada, FIFO, com redirecionamento para a DLQ)
#   wager-transactions-dlq.fifo  (fila de mensagens com falha repetida)
#   wager-events.fifo            (destino dos eventos publicados pelo serviço)

set -eu

ENDPOINT="${AWS_ENDPOINT_URL:-http://localhost:4566}"
REGION="${AWS_REGION:-us-east-1}"
export AWS_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID:-test}"
export AWS_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY:-test}"
export AWS_EC2_METADATA_DISABLED=true

MAIN_QUEUE="wager-transactions.fifo"
DLQ_QUEUE="wager-transactions-dlq.fifo"
EVENTS_QUEUE="wager-events.fifo"
MAX_RECEIVE_COUNT=5

queue_url() {
	aws --endpoint-url "$ENDPOINT" --region "$REGION" \
		sqs get-queue-url --queue-name "$1" --query 'QueueUrl' --output text 2>/dev/null || true
}

ensure_queue() {
	local name="$1" attrs_file="$2" url=""
	url="$(queue_url "$name")"
	if [ -z "$url" ]; then
		url="$(aws --endpoint-url "$ENDPOINT" --region "$REGION" \
			sqs create-queue --queue-name "$name" \
			--attributes "file://$attrs_file" \
			--query 'QueueUrl' --output text)"
		echo "created: $name" >&2
	else
		echo "exists:  $name" >&2
	fi
	printf '%s' "$url"
}

queue_arn() {
	aws --endpoint-url "$ENDPOINT" --region "$REGION" \
		sqs get-queue-attributes --queue-url "$1" --attribute-names QueueArn \
		--query 'Attributes.QueueArn' --output text
}

echo "endpoint: $ENDPOINT region: $REGION"

TMPDIR_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_ROOT"' EXIT INT TERM
printf '{"FifoQueue":"true","ContentBasedDeduplication":"false"}' >"$TMPDIR_ROOT/fifo.json"

DLQ_URL="$(ensure_queue "$DLQ_QUEUE" "$TMPDIR_ROOT/fifo.json")"
DLQ_ARN="$(queue_arn "$DLQ_URL")"
printf '{"FifoQueue":"true","ContentBasedDeduplication":"false","VisibilityTimeout":"30","RedrivePolicy":"%s"}' \
	"$(printf '{"deadLetterTargetArn":"%s","maxReceiveCount":"%s"}' "$DLQ_ARN" "$MAX_RECEIVE_COUNT" | sed 's/"/\\"/g')" \
	>"$TMPDIR_ROOT/main.json"

MAIN_URL="$(ensure_queue "$MAIN_QUEUE" "$TMPDIR_ROOT/main.json")"
aws --endpoint-url "$ENDPOINT" --region "$REGION" \
	sqs set-queue-attributes --queue-url "$MAIN_URL" \
	--attributes "file://$TMPDIR_ROOT/main.json" \
	>/dev/null
echo "attributes converged: $MAIN_QUEUE (VisibilityTimeout=30s, maxReceiveCount=$MAX_RECEIVE_COUNT)"

EVENTS_URL="$(ensure_queue "$EVENTS_QUEUE" "$TMPDIR_ROOT/fifo.json")"

echo "queues:"
echo "  $MAIN_URL"
echo "  $DLQ_URL"
echo "  $EVENTS_URL"
