# syntax=docker/dockerfile:1

# ---- build ------------------------------------------------------------------
# Versão do Go alinhada com o go.mod (go 1.27).
FROM golang:1.27-alpine AS build
WORKDIR /src

# Dependências primeiro para aproveitar o cache de camadas.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wallet-service ./cmd/wallet-service

# ---- runtime ----------------------------------------------------------------
FROM alpine:3.22
RUN apk add --no-cache ca-certificates wget && adduser -D -u 10001 app
COPY --from=build /out/wallet-service /usr/local/bin/wallet-service
USER app
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/wallet-service"]
