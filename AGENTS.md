# AGENTS.md - boundary-bytes-parser

## Build & Test Commands
- **Build**: `make build` or `go build -o bin/seeder ./cmd/seeder`
- **Build (prod)**: `make build-prod` (with optimizations)
- **Run**: `go run ./cmd/seeder -league [WPL|IPL|BBL|WBBL|SA20]`
- **Test single**: `go test ./internal/parser` or `go test ./...`
- **Clean**: `make clean`

## Architecture
Go CLI tool parsing cricket ball-by-ball CSV data into PostgreSQL using concurrent workers and bulk inserts.

**Key directories**:
- `cmd/seeder/` - CLI entry point with league selection flags
- `internal/database/` - PostgreSQL connection pooling and CRUD operations (pgx)
- `internal/parser/` - CSV parsing logic
- `internal/models/` - Data structures (Match, Delivery, MatchInfo, Team, Player, Official, PersonRegistry)
- `internal/seeder/` - Orchestration and concurrent processing

**Database**: PostgreSQL with wpl_* tables (schema matches Prisma). Uses COPY protocol for bulk inserts (~10x faster).

**Dependencies**: github.com/jackc/pgx/v5 (database), github.com/joho/godotenv (env loading)

## Code Style
- **Go version**: 1.25.0
- **Imports**: Stdlib first, then blank line, then external packages (alphabetical)
- **Naming**: PascalCase for exported types/functions, camelCase for unexported
- **Error handling**: Use `fmt.Errorf("action: %w", err)` with error wrapping
- **Context**: Pass context.Context as first parameter; use context.Background() in main
- **Concurrency**: Uses flag-controlled worker pools (default: runtime.NumCPU())
- **Env vars**: DATABASE_URL required (checked at startup). Load with godotenv first, fallback to environment