.PHONY: build run dev test test-verbose test-cover clean swagger

# Generate Swagger docs from annotations
swagger:
	swag init -g cmd/server/main.go -o docs --parseDependency

# Build the server binary
build:
	go build -o bin/server ./cmd/server

# Run the server
run: build
	./bin/server

# Run with hot reload (requires: go install github.com/air-verse/air@latest)
dev: swagger
	air

# Run all tests
test:
	go test ./...

# Run tests with verbose output
test-verbose:
	go test -v ./...

# Run tests with coverage
test-cover:
	go test -cover ./...

# Clean build artifacts
clean:
	rm -rf bin/
