# API Type Generation (Backend)

The Go backend is the **single source of truth** for API types. Swagger annotations on handler functions generate `swagger.json`, which the frontend uses to auto-generate TypeScript types.

## How it works

1. Handlers in `internal/handlers/*.go` have `@Summary`, `@Param`, `@Success`, `@Router` etc. comments (swaggo format).
2. Request/response structs have `json` tags (for serialization) and `validate` tags (for input validation).
3. `swag init` parses these annotations and produces `docs/swagger.json`.

## Regenerating swagger docs

```bash
make swagger
```

This runs:
```bash
swag init -g cmd/server/main.go -o docs --parseDependency
```

Output files (all auto-generated, gitignored):
- `docs/docs.go` — Go package imported by the router
- `docs/swagger.json` — OpenAPI 2.0 spec
- `docs/swagger.yaml` — same, YAML format

## Viewing Swagger UI

Start the server and visit:
```
http://localhost:8080/swagger/
```

## Adding a new endpoint

1. Write your handler function in `internal/handlers/`.
2. Add swagger comment annotations above the function.
3. Define request/response structs with `json` tags.
4. Add `validate` tags to request structs for input validation.
5. Run `make swagger` to regenerate docs.
6. Tell the frontend dev to run `npm run generate:types`.

## Request validation

Request structs use `go-playground/validator` tags. The `decodeAndValidate()` helper in `validate.go` decodes JSON and validates in one step:

```go
var req LoginRequest
if err := decodeAndValidate(r, &req); err != nil {
    writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
    return
}
```
