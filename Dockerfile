FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod ./
COPY main.go ./
COPY migu_plist.txt ./
RUN go build -o migu-server main.go

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/migu-server .
COPY --from=builder /app/migu_plist.txt .
EXPOSE 10000
ENV PORT=10000
CMD ["./migu-server"]
