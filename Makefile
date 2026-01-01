# Makefile
.PHONY: build run dev clean test lint

build:
	go build -o tmux-dashboard .

run: build
	./tmux-dashboard

dev:
	go run . -addr localhost:8080

clean:
	rm -f tmux-dashboard

test:
	go test ./... -v

lint:
	golangci-lint run
