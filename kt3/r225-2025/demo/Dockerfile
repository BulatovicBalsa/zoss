FROM golang:1.21-alpine AS builder

WORKDIR /app

COPY go.mod ./
RUN go mod download || true

COPY . .
RUN go mod tidy && CGO_ENABLED=0 GOOS=linux go build -o /ordering-service .

FROM alpine:3.19

RUN apk add --no-cache ca-certificates

COPY --from=builder /ordering-service /ordering-service

EXPOSE 8080

ENTRYPOINT ["/ordering-service"]