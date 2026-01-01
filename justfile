# tmux-dashboard justfile

# Build the binary
build:
    go build -o tmux-dashboard .

# Build and run the server
run: build
    ./tmux-dashboard

# Run with go run (for development)
dev:
    go run . -addr localhost:8080

# Remove build artifacts
clean:
    rm -f tmux-dashboard

# Run all tests
test:
    go test ./... -v

# Run linter
lint:
    golangci-lint run
