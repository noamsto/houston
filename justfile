# houston justfile

# Build the binary
build:
    #!/usr/bin/env bash
    test -d ui/node_modules || npm install --prefix ui
    test -d ui/dist || npm run build --prefix ui
    exec go build -o houston .

# Build and run the server
run: build
    exec ./houston

# Run with hot reload (air), finds available port
dev:
    #!/usr/bin/env bash
    test -d ui/node_modules || npm install --prefix ui
    test -d ui/dist || npm run build --prefix ui
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
    test -d ui/dist || npm run build --prefix ui
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
    test -d ui/dist || npm run build --prefix ui
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
    cd ui && exec npm run dev

# Build React frontend for production
ui-build:
    cd ui && exec npm run build

# Remove build artifacts
clean:
    exec rm -f houston

# Run all tests
test:
    exec go test ./... -v

# Run linter
lint:
    exec golangci-lint run

# Run nilaway over the module's own packages
nilaway:
    exec nilaway -include-pkgs=github.com/noamsto/houston ./...

# Run tests with the race detector
race:
    exec go test -race ./...

# The Claude Code release the read-only mod is written against
claude_code := "@anthropic-ai/claude-code@2.1.302"

# Validate and test the Claude Code mod as CI does; fails on a call on $ beyond the allowed four
mod-check:
    #!/usr/bin/env bash
    set -euo pipefail
    allowed=$(printf '%s\n' '$.clock.every' '$.env.get' '$.http.fetch' '$.session.id')
    status=0
    out=$(npx -y {{claude_code}} plugin validate contrib/claude-mod 2>&1) || status=$?
    printf '%s\n' "$out"
    [ "$status" -eq 0 ] || exit "$status"
    calls=$(printf '%s\n' "$out" | grep 'calls:' | grep -o '\$\.[a-z]*\.[a-z]*' | sort -u || true)
    if [ -z "$calls" ]; then
        echo "mod-check: validate printed no calls line" >&2
        exit 1
    fi
    extra=$(comm -13 <(printf '%s\n' "$allowed" | sort) <(printf '%s\n' "$calls"))
    if [ -n "$extra" ]; then
        echo "mod-check: the mod calls on \$ beyond the allowed set:" >&2
        printf '%s\n' "$extra" | sed 's/^/  /' >&2
        exit 1
    fi
    npx -y {{claude_code}} plugin test contrib/claude-mod
