# AGENTS.md - boundary-bytes-parser

## Build & Test Commands
- **Build**: `make build` or `go build -o bin/seeder ./cmd/seeder`
- **Build (prod)**: `make build-prod` (with optimizations)
- **Run**: `go run ./cmd/seeder` (all leagues) or `go run ./cmd/seeder -league IPL,PSL` (codes from `internal/leagues/leagues.json`)
- **Load any folder** (e.g. Cricsheet's recently_added zip): `go run ./cmd/seeder -dir /path/to/csvs`
- **Player name/slug sync only**: `make sync-players` (`-sync-only`)
- **Daily/weekly Cricsheet update**: `../scripts/cricsheet_update.sh [daily|full]`
- **Test single**: `go test ./internal/parser` or `go test ./...`
- **Clean**: `make clean`

## Architecture
Go CLI tool parsing cricket ball-by-ball CSV data into PostgreSQL using concurrent workers and bulk inserts.

**Key directories**:
- `cmd/seeder/` - CLI entry point with league selection flags
- `internal/leagues/` - `leagues.json`: league codes, Cricsheet archive folder, event names (per gender) and team renames. Single source of truth for which leagues exist. `boundary-bytes-go-backend/internal/leagues/leagues.json` is a copy; keep them identical
- `internal/people/` - Cricsheet people register (`people.csv`): registry ID -> name / unique_name
- `internal/database/` - PostgreSQL connection pooling and CRUD operations (pgx)
- `internal/parser/` - CSV parsing logic
- `internal/models/` - Data structures (Match, Delivery, MatchInfo, Team, Player, Official, PersonRegistry)
- `internal/seeder/` - Orchestration and concurrent processing

**Database**: PostgreSQL with wpl_* tables (migrations live in `boundary-bytes-go-backend/migrations`). Uses COPY protocol for bulk inserts (~10x faster).

**League resolution**: each match's league comes from its info file's `(event, gender)` looked up in `leagues.json`; unmapped events are skipped with a warning. Several leagues can share a folder (`ssm_csv2` = SSM + WSSM, `hnd_csv2` = HND + WHND).

**Player names**: names are stored as Cricsheet's `unique_name` (people.csv), which differs from `name` only where two people share a name. Every seeder run ends by syncing stored names to people.csv and refreshing `player_slug` (keyed by `registry_id`; existing slugs never change).

**Dependencies**: github.com/jackc/pgx/v5 (database), github.com/joho/godotenv (env loading)

## Code Style
- **Go version**: 1.25.0
- **Imports**: Stdlib first, then blank line, then external packages (alphabetical)
- **Naming**: PascalCase for exported types/functions, camelCase for unexported
- **Error handling**: Use `fmt.Errorf("action: %w", err)` with error wrapping
- **Context**: Pass context.Context as first parameter; use context.Background() in main
- **Concurrency**: Uses flag-controlled worker pools (default: runtime.NumCPU())
- **Env vars**: DATABASE_URL required (checked at startup). Load with godotenv first, fallback to environment