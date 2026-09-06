set dotenv-load := true

default:
    @just --list

# Generate Go code from templ components.
generate:
    templ generate

# Run any tool from cmd/<tool>.
run TOOL *ARGS:
    go run ./cmd/{{TOOL}} {{ARGS}}

# Run the file server in the current directory.
serve *ARGS:
    go run ./cmd/fileserver {{ARGS}}

# Run all tests.
test:
    go test ./...

# Format, generate, and test everything.
check: generate
    gofmt -w .
    go test ./...

# Build one tool as a standalone binary.
build TOOL="fileserver": generate
    mkdir -p bin
    go build -trimpath -o bin/{{TOOL}} ./cmd/{{TOOL}}
