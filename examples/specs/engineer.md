Build a REST API in Go for managing bookmarks. The project is called Linkbox.

## Setup

Initialize the Go module as `github.com/example/linkbox`. Set up the project structure:

```
linkbox/
├── main.go           # entry point, start server on :8080
├── store.go          # SQLite database layer
├── handlers.go       # HTTP handlers
├── handlers_test.go  # table-driven tests for all endpoints
├── go.mod
└── go.sum
```

## Dependencies

- Router: `github.com/go-chi/chi/v5`
- SQLite: `modernc.org/sqlite` (pure Go, no CGO)
- Logging: `log/slog` (stdlib)
- No ORM. Write SQL directly.

## Database schema

```sql
CREATE TABLE IF NOT EXISTS bookmarks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    url TEXT NOT NULL,
    title TEXT NOT NULL,
    tags TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

Tags are stored as a comma-separated string (e.g., "go,api,reference").

## Endpoints

### POST /bookmarks
Create a new bookmark. Request body (JSON): `{"url": "https://example.com", "title": "Example", "tags": ["go"]}`
Response: 201 Created with the bookmark. Validate: url required + valid, title required.

### GET /bookmarks
List all bookmarks. Optional `?tag=` filter. Response: 200 with JSON array.

### GET /bookmarks/{id}
Get one bookmark. Response: 200 or 404.

### DELETE /bookmarks/{id}
Delete one bookmark. Response: 204 or 404.

## Testing

Table-driven tests in `handlers_test.go`. Use `httptest.NewServer` with chi. Fresh in-memory SQLite per test.

## Workflow

1. Set up go.mod, dependencies, database layer
2. Implement one endpoint at a time
3. Write tests after each endpoint, make sure they pass
4. Commit after each endpoint
5. Run `go vet ./...` before every commit
