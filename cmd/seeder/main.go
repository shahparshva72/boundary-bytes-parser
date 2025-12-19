package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/shahparshva72/boundary-bytes-parser/internal/database"
	"github.com/shahparshva72/boundary-bytes-parser/internal/models"
	"github.com/shahparshva72/boundary-bytes-parser/internal/seeder"
)

var leagueConfigs = map[string]models.LeagueConfig{
	"WPL":  {League: "WPL", CSVDirectory: "wpl_csv2"},
	"IPL":  {League: "IPL", CSVDirectory: "ipl_csv2"},
	"BBL":  {League: "BBL", CSVDirectory: "bbl_csv2"},
	"WBBL": {League: "WBBL", CSVDirectory: "wbb_female_csv2"},
}

func main() {
	league := flag.String("league", "", "League to process (WPL, IPL, BBL, WBBL). If empty, processes all.")
	csvDir := flag.String("csv-dir", "", "Base directory containing CSV folders. Defaults to current directory.")
	concurrency := flag.Int("concurrency", runtime.NumCPU(), "Number of concurrent workers")
	skipExisting := flag.Bool("skip-existing", true, "Skip leagues that already have data")
	flag.Parse()

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

	for name, config := range leagueConfigs {
		config.CSVDirectory = filepath.Join(baseDir, config.CSVDirectory)
		leagueConfigs[name] = config
	}

	db, err := database.NewDB(dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx := context.Background()

	var configsToProcess []models.LeagueConfig
	if *league != "" {
		leagueUpper := strings.ToUpper(*league)
		if config, ok := leagueConfigs[leagueUpper]; ok {
			configsToProcess = append(configsToProcess, config)
		} else {
			fmt.Fprintf(os.Stderr, "Error: Invalid league '%s'. Valid options: WPL, IPL, BBL, WBBL\n", *league)
			os.Exit(1)
		}
	} else {
		for _, config := range leagueConfigs {
			configsToProcess = append(configsToProcess, config)
		}
	}

	fmt.Println("Starting multi-league seed process...")
	fmt.Printf("Concurrency: %d workers\n", *concurrency)
	startTime := time.Now()

	for _, config := range configsToProcess {
		fmt.Printf("\nProcessing %s data from %s\n", config.League, config.CSVDirectory)

		s := seeder.NewSeeder(db, config, *concurrency)
		if err := s.Run(ctx, *skipExisting); err != nil {
			fmt.Fprintf(os.Stderr, "Error processing %s: %v\n", config.League, err)
		}
	}

	elapsed := time.Since(startTime)
	fmt.Printf("\nMulti-league seed completed in %s\n", elapsed.Round(time.Second))
}
