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
umask 077

ENDPOINT="${AWS_ENDPOINT_URL:-http://localhost:4566}"
REGION="${AWS_REGION:-us-east-1}"
export AWS_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID:-test}"
export AWS_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY:-test}"
export AWS_EC2_METADATA_DISABLED=true

MAIN_QUEUE="wager-transactions.fifo"
DLQ_QUEUE="wager-transactions-dlq.fifo"
EVENTS_QUEUE="wager-events.fifo"
MAX_RECEIVE_COUNT=5
ACCOUNT_ID="$(aws --endpoint-url "$ENDPOINT" --region "$REGION" \
	sts get-caller-identity --query Account --output text)"

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

# O administrador local cria usuários limitados. As credenciais geradas ficam
# fora do Git e são reutilizadas enquanto o MiniStack mantiver seu estado.
ensure_user() {
	local name="$1" policy="$2" policy_file="$TMPDIR_ROOT/$1-policy.json"
	if ! aws --endpoint-url "$ENDPOINT" --region "$REGION" \
		iam get-user --user-name "$name" >/dev/null 2>&1; then
		aws --endpoint-url "$ENDPOINT" --region "$REGION" \
			iam create-user --user-name "$name" >/dev/null
	fi
	sed "s/111111111111/$ACCOUNT_ID/g; s/us-east-1/$REGION/g" \
		"/policies/$policy" >"$policy_file"
	aws --endpoint-url "$ENDPOINT" --region "$REGION" \
		iam put-user-policy --user-name "$name" --policy-name WagerQueues \
		--policy-document "file://$policy_file" >/dev/null
}

ensure_credentials() {
	local name="$1" path="$2" old_keys="" pair=""
	if [ -f "$path" ] && (
		set -a
		. "$path"
		set +a
		aws --endpoint-url "$ENDPOINT" --region "$REGION" \
			sts get-caller-identity --query Arn --output text 2>/dev/null \
			| grep -Fq ":user/$name"
	); then
		return
	fi
	old_keys="$(aws --endpoint-url "$ENDPOINT" --region "$REGION" \
		iam list-access-keys --user-name "$name" \
		--query 'AccessKeyMetadata[].AccessKeyId' --output text)"
	for key in $old_keys; do
		[ "$key" = None ] && continue
		aws --endpoint-url "$ENDPOINT" --region "$REGION" \
			iam delete-access-key --user-name "$name" --access-key-id "$key" >/dev/null
	done
	pair="$(aws --endpoint-url "$ENDPOINT" --region "$REGION" \
		iam create-access-key --user-name "$name" \
		--query 'AccessKey.[AccessKeyId,SecretAccessKey]' --output text)"
	set -- $pair
	if [ "$#" -ne 2 ]; then
		echo "credenciais incompletas para $name" >&2
		exit 1
	fi
	printf 'AWS_ACCESS_KEY_ID=%s\nAWS_SECRET_ACCESS_KEY=%s\n' "$1" "$2" >"$path"
	chmod 600 "$path"
	chown "${HOST_UID:-1000}:${HOST_GID:-1000}" "$path"
}

ensure_user wager-service service.policy.json
ensure_user wager-producer producer.policy.json
ensure_credentials wager-service /credentials/sqs-service.env
ensure_credentials wager-producer /credentials/sqs-producer.env

sed "s/111111111111/$ACCOUNT_ID/g; s/us-east-1/$REGION/g" \
	/policies/local-input-queue.policy.json >"$TMPDIR_ROOT/queue-policy.json"
POLICY_ESCAPED="$(sed 's/"/\\"/g' "$TMPDIR_ROOT/queue-policy.json" | tr -d '\n')"
printf '{"Policy":"%s"}' "$POLICY_ESCAPED" >"$TMPDIR_ROOT/queue-policy-attrs.json"
aws --endpoint-url "$ENDPOINT" --region "$REGION" \
	sqs set-queue-attributes --queue-url "$MAIN_URL" \
	--attributes "file://$TMPDIR_ROOT/queue-policy-attrs.json" >/dev/null

echo "queues:"
echo "  $MAIN_URL"
echo "  $DLQ_URL"
echo "  $EVENTS_URL"
echo "authorization: users wager-service and wager-producer; input queue policy applied"
