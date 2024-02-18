FROM golang:1.21 AS build

WORKDIR /app

COPY . .

RUN CGO_ENABLED=0 go build -o /cloudflare-ddns

FROM scratch as final

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /cloudflare-ddns /cloudflare-ddns

ENTRYPOINT ["/cloudflare-ddns"]