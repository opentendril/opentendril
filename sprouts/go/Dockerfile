FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
COPY sprouts/go/main.go ./sprouts/go/main.go

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/sprout-go ./sprouts/go

FROM alpine:3.20

RUN apk add --no-cache ca-certificates git

COPY --from=build /out/sprout-go /usr/local/bin/sprout-go

WORKDIR /app

ENTRYPOINT ["/usr/local/bin/sprout-go"]
