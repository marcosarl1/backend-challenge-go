#!/usr/bin/env sh
# Lê a credencial do serviço em tempo de start (não via env_file do Compose,
# que é avaliado antes do sqs-init rotacionar as chaves) e entrega o sinal
# ao binário via exec.
set -eu

if [ -f /credentials/sqs-service.env ]; then
	set -a
	# shellcheck disable=SC1091
	. /credentials/sqs-service.env
	set +a
fi

if [ "$(id -u)" = "0" ] && command -v su-exec >/dev/null 2>&1; then
	exec su-exec app /usr/local/bin/wagering "$@"
fi

exec /usr/local/bin/wagering "$@"
