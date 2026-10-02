# Identidade (Keycloak)

O ambiente local sobe um Keycloak com o realm `wagering` já importado:

```sh
docker compose up -d keycloak
```

Imagem fixada no `compose.yaml` (`quay.io/keycloak/keycloak:26.7.5`), com o
realm em `deploy/keycloak/realm-export.json`. Recriar o container reimporta
tudo do zero, sem passo manual.

## Clientes (todos `client_credentials`, confidenciais)

| Client | Segredo local | Papel | Claim extra |
| --- | --- | --- | --- |
| `provider-a` | `provider-a-secret` | `provider` | `provider_id = provider-a` |
| `provider-b` | `provider-b-secret` | `provider-b` | `provider_id = provider-b` |
| `wallet-internal` | `wallet-internal-secret` | `internal` | — |

Os segredos acima são valores de teste para ambiente local, sem valor em
produção. Todos os tokens trazem audiência `wagering-api` e emissor
`http://localhost:8080/realms/wagering`.

## Pegando um token

```sh
curl -s http://localhost:8080/realms/wagering/protocol/openid-connect/token \
  -d grant_type=client_credentials \
  -d client_id=provider-a \
  -d client_secret=provider-a-secret | python3 -c "import json,sys; print(json.load(sys.stdin)['access_token'])"
```

Troque client_id/secret para `provider-b` ou `wallet-internal` conforme o
fluxo. Segredo errado responde 401.

Console administrativo (local): http://localhost:8080/admin — usuário `admin`,
senha `admin`.
