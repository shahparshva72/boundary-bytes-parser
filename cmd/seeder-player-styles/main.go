package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"github.com/shahparshva72/boundary-bytes-parser/internal/database"
	"github.com/shahparshva72/boundary-bytes-parser/internal/models"
)

func main() {
	csvFile := flag.String("input", "player_styles.csv", "Path to player_styles.csv")
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

	inputPath := *csvFile
	if !filepath.IsAbs(inputPath) {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting current directory: %v\n", err)
			os.Exit(1)
		}
		inputPath = filepath.Join(cwd, inputPath)
	}

	styles, err := parsePlayerStylesCSV(inputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing CSV: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Parsed %d player styles from %s\n", len(styles), inputPath)

	db, err := database.NewDB(dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx := context.Background()

	startTime := time.Now()

	inserted, err := db.UpsertPlayerStylesBulk(ctx, styles)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error inserting player styles: %v\n", err)
		os.Exit(1)
	}

	elapsed := time.Since(startTime)
	fmt.Printf("Successfully inserted %d player styles in %s\n", inserted, elapsed.Round(time.Millisecond))

	count, err := db.GetPlayerStyleCount(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to verify count: %v\n", err)
	} else {
		fmt.Printf("Verified: %d rows in player_style table\n", count)
	}
}

// parsePlayerStylesCSV reads and parses the player_styles.csv file.
// CSV columns: identifier, key_cricinfo, name, full_name, batting_hand,
// bowling_hand, bowling_type, bowling_sub_type, playing_role, playing_role_detail,
// batting_style_raw, bowling_style_raw
func parsePlayerStylesCSV(path string) ([]models.PlayerStyle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.LazyQuotes = true

	// Read and validate header
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("failed to read header: %w", err)
	}

	expectedCols := 12
	if len(header) != expectedCols {
		return nil, fmt.Errorf("expected %d columns, got %d", expectedCols, len(header))
	}

	var styles []models.PlayerStyle
	lineNum := 1

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping line %d: %v\n", lineNum+1, err)
			lineNum++
			continue
		}
		lineNum++

		if len(record) != expectedCols {
			fmt.Fprintf(os.Stderr, "Warning: skipping line %d: expected %d columns, got %d\n", lineNum, expectedCols, len(record))
			continue
		}

		keyCricinfo, err := strconv.Atoi(record[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping line %d: invalid key_cricinfo %q: %v\n", lineNum, record[1], err)
			continue
		}

		style := models.PlayerStyle{
			Identifier:        record[0],
			KeyCricinfo:       keyCricinfo,
			Name:              record[2],
			FullName:          nullableString(record[3]),
			BattingHand:       nullableString(record[4]),
			BowlingHand:       nullableString(record[5]),
			BowlingType:       nullableString(record[6]),
			BowlingSubType:    nullableString(record[7]),
			PlayingRole:       record[8],
			PlayingRoleDetail: record[9],
			BattingStyleRaw:   nullableString(record[10]),
			BowlingStyleRaw:   nullableString(record[11]),
		}

		styles = append(styles, style)
	}

	return styles, nil
}

// nullableString returns nil for empty strings, otherwise a pointer to the string.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
