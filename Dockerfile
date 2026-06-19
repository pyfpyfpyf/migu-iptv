FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o migu-server main.go

FROM scratch
COPY --from=builder /app/migu-server .
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY migu_plist.txt .
EXPOSE 8080
CMD ["./migu-server"]
