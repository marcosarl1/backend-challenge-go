FROM golang:1.27-alpine AS build

WORKDIR /src

ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64

COPY go.mod ./
COPY go.sum* ./
RUN go mod download

COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/wagering ./cmd/wagering

FROM alpine:3.20

RUN apk add --no-cache su-exec && addgroup -S app && adduser -S app -G app

COPY --from=build /out/wagering /usr/local/bin/wagering
COPY deploy/app/docker-entrypoint.sh /entrypoint.sh

# Sem `USER` aqui: o entrypoint lê a credencial do serviço (arquivo 600 do
# host, legível só para root) e entrega a execução ao usuário `app`.
ENTRYPOINT ["/entrypoint.sh"]
