package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/shahparshva72/boundary-bytes-parser/internal/database"
	"github.com/shahparshva72/boundary-bytes-parser/internal/leagues"
	"github.com/shahparshva72/boundary-bytes-parser/internal/people"
	"github.com/shahparshva72/boundary-bytes-parser/internal/seeder"
)

func main() {
	league := flag.String("league", "", "Comma-separated league codes from leagues.json (e.g. IPL,PSL). If empty, processes all.")
	csvDir := flag.String("csv-dir", "", "Base directory containing the Cricsheet csv2 folders. Defaults to current directory.")
	dir := flag.String("dir", "", "Load one directory of Cricsheet csv2 files (e.g. an extracted recently_added zip) instead of the league folders. Leagues are still resolved per match.")
	peopleFile := flag.String("people", "", "Path to Cricsheet's people.csv register. Defaults to people.csv in -csv-dir.")
	concurrency := flag.Int("concurrency", runtime.NumCPU(), "Number of concurrent workers")
	skipExisting := flag.Bool("skip-existing", true, "Skip matches that are already loaded")
	syncOnly := flag.Bool("sync-only", false, "Skip loading; only sync player names to people.csv and refresh player slugs")
	listSources := flag.Bool("list-sources", false, "Print the Cricsheet archive names from leagues.json, one per line, and exit")
	flag.Parse()

	registry, err := leagues.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading leagues.json: %v\n", err)
		os.Exit(1)
	}

	if *listSources {
		for _, source := range registry.Sources() {
			fmt.Println(source)
		}
		return
	}

	if err := godotenv.Load(); err != nil {
		if err := godotenv.Load("../.env"); err != nil {
			fmt.Println("Warning: No .env file found, using environment variables")
		}
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "Error: DATABASE_URL environment variable is required")
		os.Exit(1)
	}

	baseDir := *csvDir
	if baseDir == "" {
		var err error
		baseDir, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
			os.Exit(1)
		}
	}
	if *peopleFile == "" {
		*peopleFile = filepath.Join(baseDir, "people.csv")
	}

	var only map[string]bool
	directories := registry.Sources()
	if *league != "" {
		codes := strings.Split(strings.ToUpper(*league), ",")
		for i := range codes {
			codes[i] = strings.TrimSpace(codes[i])
		}
		directories, err = registry.SourcesFor(codes)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		only = make(map[string]bool, len(codes))
		for _, c := range codes {
			only[c] = true
		}
	}
	for i, d := range directories {
		directories[i] = filepath.Join(baseDir, d)
	}
	if *dir != "" {
		directories = []string{*dir}
	}

	register, err := people.Load(*peopleFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading people register: %v\n", err)
		os.Exit(1)
	}

	poolSize := int32(*concurrency + 4)
	if poolSize < 10 {
		poolSize = 10
	}

	db, err := database.NewDB(dbURL, poolSize)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx := context.Background()
	startTime := time.Now()

	if !*syncOnly {
		fmt.Println("Starting multi-league seed process...")
		fmt.Printf("Concurrency: %d workers\n", *concurrency)

		for _, d := range directories {
			fmt.Printf("\nProcessing %s\n", d)

			s := seeder.NewSeeder(db, d, registry, only, register, *concurrency)
			if err := s.Run(ctx, *skipExisting); err != nil {
				fmt.Fprintf(os.Stderr, "Error processing %s: %v\n", d, err)
			}
		}
	}

	if err := syncPlayers(ctx, db, register.UniqueNames()); err != nil {
		fmt.Fprintf(os.Stderr, "Error syncing players: %v\n", err)
		os.Exit(1)
	}

	elapsed := time.Since(startTime)
	fmt.Printf("\nMulti-league seed completed in %s\n", elapsed.Round(time.Second))
}

// syncPlayers keeps player identity consistent after every load: legacy slugs
// are linked to people while their names still match, stored names are made
// to match Cricsheet's unique_name, then slugs are created/updated.
func syncPlayers(ctx context.Context, db *database.DB, uniqueNames map[string]string) error {
	fmt.Println("\nSyncing players...")

	linked, err := db.AssignLegacySlugRegistryIDs(ctx)
	if err != nil {
		return err
	}
	if linked > 0 {
		fmt.Printf("  Linked %d existing slugs to Cricsheet registry IDs\n", linked)
	}

	names, err := db.SyncUniqueNames(ctx, uniqueNames)
	if err != nil {
		return err
	}
	fmt.Printf("  Unique-name renames: %d (match, name) pairs\n", names.Renames)
	columns := make([]string, 0, len(names.Rows))
	for c := range names.Rows {
		columns = append(columns, c)
	}
	sort.Strings(columns)
	for _, c := range columns {
		if names.Rows[c] > 0 {
			fmt.Printf("    %s: %d rows\n", c, names.Rows[c])
		}
	}

	slugs, err := db.RefreshPlayerSlugs(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("  Player slugs: %d players, %d new, %d updated\n", slugs.Players, slugs.Inserted, slugs.Updated)
	if len(slugs.Removed) > 0 {
		fmt.Printf("  Removed %d duplicate legacy slugs (person already has a profile): %s\n",
			len(slugs.Removed), strings.Join(slugs.Removed, ", "))
	}
	if slugs.Unregistered > 0 {
		fmt.Printf("  WARNING: %d batting/bowling appearances have no registry entry and no profile\n", slugs.Unregistered)
	}

	return nil
}
