FROM golang:1.27 AS build

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -o /cloudflare-ddns

FROM scratch AS final

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /cloudflare-ddns /cloudflare-ddns

ENTRYPOINT ["/cloudflare-ddns"]