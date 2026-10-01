FROM golang:1.27-alpine AS build

WORKDIR /src

ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64

COPY go.mod ./
COPY go.sum* ./
RUN go mod download

COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/wagering ./cmd/wagering

FROM alpine:3.20

RUN addgroup -S app && adduser -S app -G app

COPY --from=build /out/wagering /usr/local/bin/wagering

USER app

ENTRYPOINT ["/usr/local/bin/wagering"]
