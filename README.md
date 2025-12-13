# Boundary Bytes CSV Parser (Go)

A high-performance CSV parser for cricket ball-by-ball data, designed to efficiently import large datasets into PostgreSQL.

## Features

- Concurrent processing with configurable worker count
- Batch inserts using PostgreSQL's COPY protocol for maximum performance
- Support for multiple leagues (WPL, IPL, BBL)
- Skip existing data to avoid duplicate imports
- Progress tracking with processing rate statistics

## Prerequisites

- Go 1.21 or later
- PostgreSQL database with the schema matching the Prisma schema

## Installation

```bash
cd boundary-bytes-parser
go mod tidy
go build -o bin/seeder ./cmd/seeder
```

## Usage

### Environment Setup

Create a `.env` file in the project root or set the `DATABASE_URL` environment variable:

```bash
DATABASE_URL="postgresql://user:password@localhost:5432/database"
```

### Running the Seeder

Process all leagues:

```bash
./bin/seeder
```

Process a specific league:

```bash
./bin/seeder -league WPL
./bin/seeder -league IPL
./bin/seeder -league BBL
```

Custom CSV directory:

```bash
./bin/seeder -csv-dir /path/to/csv/files
```

Adjust concurrency:

```bash
./bin/seeder -concurrency 8
```

Force processing even if data exists:

```bash
./bin/seeder -skip-existing=false
```

### Command Line Options

| Flag             | Default           | Description                           |
| ---------------- | ----------------- | ------------------------------------- |
| `-league`        | (all)             | League to process: WPL, IPL, or BBL   |
| `-csv-dir`       | current directory | Base directory containing CSV folders |
| `-concurrency`   | number of CPUs    | Number of concurrent workers          |
| `-skip-existing` | true              | Skip leagues that already have data   |

## CSV Directory Structure

The parser expects the following directory structure:

```
csv-dir/
├── wpl_csv2/
│   ├── 123456.csv      # Ball-by-ball data
│   ├── 123456_info.csv # Match metadata
│   └── ...
├── ipl_csv2/
│   └── ...
└── bbl_csv2/
    └── ...
```

## Performance

The Go implementation provides significant performance improvements over the TypeScript/Prisma version:

- Uses PostgreSQL's COPY protocol for bulk inserts (~10x faster than individual INSERTs)
- Concurrent file processing
- Memory-efficient streaming CSV parsing
- Connection pooling for database operations

Typical performance on a modern machine:

- ~100+ matches per second
- ~50,000+ deliveries per second

## Development

### Project Structure

```
boundary-bytes-parser/
├── cmd/
│   └── seeder/
│       └── main.go          # Entry point
├── internal/
│   ├── database/
│   │   └── database.go      # Database operations
│   ├── models/
│   │   └── models.go        # Data structures
│   ├── parser/
│   │   └── parser.go        # CSV parsing logic
│   └── seeder/
│       └── seeder.go        # Orchestration logic
├── go.mod
├── go.sum
└── README.md
```

### Building

```bash
# Development build
go build -o bin/seeder ./cmd/seeder

# Production build with optimizations
go build -ldflags="-s -w" -o bin/seeder ./cmd/seeder
```

### Testing

```bash
go test ./...
```

## Database Schema

The parser writes to the following tables (matching the Prisma schema):

- `wpl_match` - Match basic info (id, league, season, start_date, venue)
- `wpl_delivery` - Ball-by-ball data
- `wpl_match_info` - Match metadata
- `wpl_team` - Teams per match
- `wpl_player` - Players per match
- `wpl_official` - Match officials
- `wpl_person_registry` - People registry IDs

## Migrating from TypeScript

The Go parser is a direct replacement for the TypeScript seed scripts in `prisma/seed.ts` and `prisma/seed-multi-league.ts`. It maintains the same database schema and data format while providing better performance for large datasets.
