# tmux-dashboard justfile

# Generate templ files
generate:
    templ generate

# Build the binary
build: generate
    go build -o tmux-dashboard .

# Build and run the server
run: build
    ./tmux-dashboard

# Run with hot reload (air), finds available port
dev:
    #!/usr/bin/env bash
    for port in 8080 8081 8082 8083 8084 8085; do
        if ! nc -z localhost $port 2>/dev/null; then
            export TMUX_DASHBOARD_PORT=$port
            exec air
        fi
    done
    echo "No available port found in range 8080-8085"
    exit 1

# Run without hot reload, finds available port
run-dev:
    #!/usr/bin/env bash
    for port in 8080 8081 8082 8083 8084 8085; do
        if ! nc -z localhost $port 2>/dev/null; then
            exec go run . -addr 0.0.0.0:$port
        fi
    done
    echo "No available port found in range 8080-8085"
    exit 1

# Remove build artifacts
clean:
    rm -f tmux-dashboard

# Run all tests
test:
    go test ./... -v

# Run linter
lint:
    golangci-lint run
