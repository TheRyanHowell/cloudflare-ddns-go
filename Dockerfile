FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Cross-compile for the target platform rather than emulating it.
ARG TARGETOS TARGETARCH TARGETVARIANT
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /cloudflare-ddns ./cmd/cloudflare-ddns

FROM scratch AS final

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /cloudflare-ddns /cloudflare-ddns

USER 65532:65532

ENTRYPOINT ["/cloudflare-ddns"]