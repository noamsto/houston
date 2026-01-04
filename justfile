# houston justfile

# Generate templ files
generate:
    templ generate

# Build the binary
build: generate
    go build -o houston .

# Build and run the server
run: build
    ./houston

# Run with hot reload (air), finds available port
dev:
    #!/usr/bin/env bash
    for port in 9090 9091 9092 9093 9094 9095; do
        if ! nc -z localhost $port 2>/dev/null; then
            export HOUSTON_PORT=$port
            echo "Starting houston on port $port"
            exec air
        fi
    done
    echo "No available port found in range 9090-9095"
    exit 1

# Run without hot reload, finds available port
run-dev:
    #!/usr/bin/env bash
    for port in 9090 9091 9092 9093 9094 9095; do
        if ! nc -z localhost $port 2>/dev/null; then
            echo "Starting houston on port $port"
            exec go run . -addr 0.0.0.0:$port
        fi
    done
    echo "No available port found in range 9090-9095"
    exit 1

# Remove build artifacts
clean:
    rm -f houston

# Run all tests
test:
    go test ./... -v

# Run linter
lint:
    golangci-lint run
