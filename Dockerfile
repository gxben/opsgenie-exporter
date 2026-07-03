FROM golang:1.24-alpine AS builder

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -trimpath \
      -o /bin/opsgenie-exporter \
      .

# ─────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="opsgenie-exporter" \
      org.opencontainers.image.description="Prometheus exporter for OpsGenie" \
      org.opencontainers.image.url="https://github.com/gxben/opsgenie-exporter" \
      org.opencontainers.image.source="https://github.com/gxben/opsgenie-exporter"

COPY --from=builder /bin/opsgenie-exporter /opsgenie-exporter

EXPOSE 9428
USER nonroot:nonroot

ENTRYPOINT ["/opsgenie-exporter"]
