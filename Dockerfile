# syntax=docker/dockerfile:1
FROM golang:1.26.8-alpine AS build
ARG VERSION=dev
ENV CGO_ENABLED=0
WORKDIR /src
COPY go.* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/http-api-spec ./cmd/http-api-spec

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/http-api-spec /http-api-spec
WORKDIR /work
USER nonroot:nonroot
ENTRYPOINT ["/http-api-spec"]
