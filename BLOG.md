# Boundary Bytes: Seeding Five Cricket Leagues in 11 Seconds

On a fresh run, the Go seeder boots up, detects each league dataset, and fans out work across CPU cores. The result is a fast, repeatable import pipeline for ball-by-ball cricket data that fills PostgreSQL with match metadata and deliveries at scale.

## The First Run: Schema Reality Check

The seeder is honest about prerequisites. A first run against an empty database produced an immediate warning:

```text
Error processing WPL: failed to get existing match IDs: ERROR: relation "wpl_match" does not exist (SQLSTATE 42P01)
```

That message is exactly what you want from a data pipeline. It tells you the schema must exist before importing. Once the database tables are in place, the same command becomes a high-speed ingestion engine.

## The Multi-League Sprint

With the schema ready, `make run` kicks off a multi-league seed with concurrency set to 8 workers. The seeder processes match info files first, then streams delivery CSVs, reporting steady throughput throughout.

Here is the actual run summary from the console:

```text
WPL seed completed: 87 matches and 20410 deliveries processed
IPL seed completed: 1169 matches and 278205 deliveries processed
BBL seed completed: 662 matches and 153250 deliveries processed
WBBL seed completed: 502 matches and 115020 deliveries processed
SA20 seed completed: 130 matches and 29020 deliveries processed

Multi-league seed completed in 11s
```

That is 2,550 matches and 595,905 deliveries ingested in a single pass.

## What Makes It Fast

This repository is designed for bulk throughput, not one-off inserts. The run output reflects a few key choices in the codebase:

- PostgreSQL COPY protocol for bulk inserts instead of row-by-row writes.
- Concurrent worker pools to parse and ingest CSV files in parallel.
- Progress logging that shows rate stability over long runs.

The IPL run alone sustained ~320 matches per second for more than a thousand match files.

## The Workflow in Practice

The operational flow is intentionally simple:

1. Ensure the Postgres schema exists (tables like `wpl_match`, `wpl_delivery`, and `wpl_match_info`).
2. Run `make run` to seed all leagues, or use a league-specific target (for example, `make run-ipl`).
3. Watch the progress ticker for match counts and throughput.

In 11 seconds, a local database is populated with five leagues worth of ball-by-ball data, ready for analytics or downstream services.

## Why This Matters

Cricket data is huge, and it is often siloed in CSVs. The Boundary Bytes parser turns those files into a queryable, normalized schema with consistent throughput and reproducibility. That reliability lets you iterate faster on models, dashboards, and data products without fighting the data load every time.

If you are curious, the codebase lives in the same repository, and `cmd/seeder` is the entry point that drives the whole flow.
