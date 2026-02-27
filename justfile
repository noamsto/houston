# houston justfile

# Build the binary
build:
    go build -o houston .

# Build and run the server
run: build
    ./houston

# Run with hot reload (air), finds available port
dev:
    #!/usr/bin/env bash
    test -d ui/node_modules || npm install --prefix ui
    for port in 7474 7475 7476 7477 7478 7479; do
        if ! nc -z localhost $port 2>/dev/null; then
            export HOUSTON_PORT=$port
            echo "Starting houston on port $port"
            exec air
        fi
    done
    echo "No available port found in range 7474-7479"
    exit 1

# Run without hot reload, finds available port
run-dev:
    #!/usr/bin/env bash
    test -d ui/node_modules || npm install --prefix ui
    for port in 7474 7475 7476 7477 7478 7479; do
        if ! nc -z localhost $port 2>/dev/null; then
            echo "Starting houston on port $port"
            exec go run . -addr 0.0.0.0:$port
        fi
    done
    echo "No available port found in range 7474-7479"
    exit 1

# Run with localhost binding only (use with Tailscale serve)
dev-local:
    #!/usr/bin/env bash
    test -d ui/node_modules || npm install --prefix ui
    for port in 7474 7475 7476 7477 7478 7479; do
        if ! nc -z localhost $port 2>/dev/null; then
            export HOUSTON_PORT=$port
            export HOUSTON_ADDR="127.0.0.1:$port"
            echo "Starting houston on localhost:$port (Tailscale: run 'tailscale serve --bg $port')"
            exec air
        fi
    done
    echo "No available port found in range 7474-7479"
    exit 1

# Start React dev server (proxy to Go backend at :9090)
ui-dev:
    cd ui && npm run dev

# Build React frontend for production
ui-build:
    cd ui && npm run build

# Remove build artifacts
clean:
    rm -f houston

# Run all tests
test:
    go test ./... -v

# Run linter
lint:
    golangci-lint run
