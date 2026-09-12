# syntax=docker/dockerfile:1

# Tek Dockerfile, iki binary: --build-arg CMD=collector|api
ARG GO_VERSION=1.27

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

# Bağımlılıklar ayrı katmanda: kaynak değişince yeniden indirilmesin.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG CMD=collector
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/nabiz ./cmd/${CMD}

# Statik binary; dağıtımın kendisi ayak izi olmasın diye scratch değil
# alpine: shell ve CA sertifikaları hata ayıklamada işe yarıyor.
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 nabiz
COPY --from=build /out/nabiz /usr/local/bin/nabiz
USER 10001
ENTRYPOINT ["/usr/local/bin/nabiz"]
