# Makefile for Tendril CLI

.PHONY: build clean

build:
	go mod init tendril-cli || true
	go mod tidy
	go build -o tendril-cli main.go

clean:
	rm -f tendril-cli go.mod go.sum
