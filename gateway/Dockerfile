# Build stage
FROM golang:1.23-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o gateway ./cmd/gateway

# Runtime stage — ~10MB final image
FROM alpine:3.19
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /build/gateway .
EXPOSE 9090
ENTRYPOINT ["./gateway"]
