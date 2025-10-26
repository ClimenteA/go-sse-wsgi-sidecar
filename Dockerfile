# BUILD
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum main.go ./

RUN go mod download

RUN go build -o sse-sidecar main.go

# RUN
FROM alpine:3.18

RUN apk add --no-cache ca-certificates

WORKDIR /app

COPY --from=builder /app/sse-sidecar .

EXPOSE 5687

CMD ["./sse-sidecar"]
