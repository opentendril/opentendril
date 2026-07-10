FROM golang:1.24-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
COPY sprouts/go/main.go ./sprouts/go/main.go

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/tendril-go ./sprouts/go

FROM alpine:3.20

RUN apk add --no-cache ca-certificates git

COPY --from=build /out/tendril-go /usr/local/bin/tendril-go

WORKDIR /app

ENTRYPOINT ["/usr/local/bin/tendril-go"]
