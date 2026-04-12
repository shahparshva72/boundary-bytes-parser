package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	espnCoreAPI    = "http://core.espnuk.org/v2/sports/cricket/athletes/%s"
	defaultTimeout = 10 * time.Second
	defaultDelay   = 100 * time.Millisecond // per-worker delay between requests
)

// PlayerStyle represents a batting or bowling style from the ESPN API.
type PlayerStyle struct {
	Description      string `json:"description"`
	ShortDescription string `json:"shortDescription"`
	Type             string `json:"type"`
}

// PlayerPosition represents the playing role from the ESPN API.
type PlayerPosition struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Abbreviation string `json:"abbreviation"`
}

// ESPNPlayerResponse represents the relevant fields from the ESPN Core API.
type ESPNPlayerResponse struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	FullName string          `json:"fullName"`
	Style    []PlayerStyle   `json:"style"`
	Styles   []PlayerStyle   `json:"styles"`
	Position *PlayerPosition `json:"position"`
	Gender   string          `json:"gender"`
	Active   bool            `json:"active"`
}

// PlayerResult holds the scraped result for a single player.
type PlayerResult struct {
	Identifier   string
	Name         string
	KeyCricinfo  string
	FullName     string
	BattingStyle string
	BowlingStyle string
	PlayingRole  string
	Error        string
}

func main() {
	inputFile := flag.String("input", "people.csv", "Path to the input people.csv file")
	outputFile := flag.String("output", "player_attributes.csv", "Path to the output CSV file")
	workers := flag.Int("workers", 10, "Number of concurrent workers")
	delay := flag.Duration("delay", defaultDelay, "Delay between requests per worker (e.g. 100ms)")
	limit := flag.Int("limit", 0, "Limit number of players to scrape (0 = all)")
	flag.Parse()

	fmt.Println("=== ESPNcricinfo Player Attribute Scraper ===")
	fmt.Printf("Input:   %s\n", *inputFile)
	fmt.Printf("Output:  %s\n", *outputFile)
	fmt.Printf("Workers: %d\n", *workers)
	fmt.Printf("Delay:   %s per worker\n", *delay)

	// Read input CSV
	players, err := readPeopleCSV(*inputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading input CSV: %v\n", err)
		os.Exit(1)
	}

	// Filter to only players with a cricinfo ID
	var playersWithID [][]string
	for _, row := range players {
		cricinfoID := row[2] // key_cricinfo extracted during CSV read
		if cricinfoID != "" {
			playersWithID = append(playersWithID, row)
		}
	}

	fmt.Printf("Total players in CSV: %d\n", len(players))
	fmt.Printf("Players with key_cricinfo: %d\n", len(playersWithID))

	if *limit > 0 && *limit < len(playersWithID) {
		playersWithID = playersWithID[:*limit]
		fmt.Printf("Limited to: %d players\n", *limit)
	}

	// Load already-scraped IDs to support resume
	existingIDs := loadExistingIDs(*outputFile)
	if len(existingIDs) > 0 {
		fmt.Printf("Found %d already-scraped players in %s, will skip them\n", len(existingIDs), *outputFile)
	}

	// Filter out already-scraped
	var toScrape [][]string
	for _, row := range playersWithID {
		cricinfoID := row[2]
		if _, exists := existingIDs[cricinfoID]; !exists {
			toScrape = append(toScrape, row)
		}
	}

	fmt.Printf("Players to scrape: %d\n", len(toScrape))
	if len(toScrape) == 0 {
		fmt.Println("Nothing to scrape. Exiting.")
		return
	}

	// Set up HTTP client
	client := &http.Client{
		Timeout: defaultTimeout,
	}

	// Set up worker pool
	jobs := make(chan []string, len(toScrape))
	results := make(chan PlayerResult, len(toScrape))

	var scraped atomic.Int64
	total := int64(len(toScrape))
	startTime := time.Now()

	var wg sync.WaitGroup
	for i := range *workers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for row := range jobs {
				identifier := row[0]
				name := row[1]
				cricinfoID := row[2]

				result := scrapePlayer(client, identifier, name, cricinfoID)
				results <- result

				count := scraped.Add(1)
				if count%100 == 0 || count == total {
					elapsed := time.Since(startTime)
					rate := float64(count) / elapsed.Seconds()
					remaining := time.Duration(float64(total-count)/rate) * time.Second
					fmt.Printf("[%d/%d] %.1f/s | elapsed: %s | remaining: ~%s\n",
						count, total, rate, elapsed.Round(time.Second), remaining.Round(time.Second))
				}

				time.Sleep(*delay)
			}
		}(i)
	}

	// Feed jobs
	go func() {
		for _, row := range toScrape {
			jobs <- row
		}
		close(jobs)
	}()

	// Collect results in background
	go func() {
		wg.Wait()
		close(results)
	}()

	// Write results to CSV (append mode for resume support)
	if err := writeResults(*outputFile, results, len(existingIDs) == 0); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output CSV: %v\n", err)
		os.Exit(1)
	}

	elapsed := time.Since(startTime)
	fmt.Printf("\nDone! Scraped %d players in %s\n", scraped.Load(), elapsed.Round(time.Second))
	fmt.Printf("Output saved to: %s\n", *outputFile)
}

// readPeopleCSV reads the people.csv and returns rows of [identifier, name, key_cricinfo].
func readPeopleCSV(path string) ([][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening file: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)

	// Read header to find column indices
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}

	identifierIdx := -1
	nameIdx := -1
	cricinfoIdx := -1

	for i, col := range header {
		switch strings.TrimSpace(col) {
		case "identifier":
			identifierIdx = i
		case "name":
			nameIdx = i
		case "key_cricinfo":
			cricinfoIdx = i
		}
	}

	if identifierIdx == -1 || nameIdx == -1 || cricinfoIdx == -1 {
		return nil, fmt.Errorf("missing required columns (need: identifier, name, key_cricinfo), found: %v", header)
	}

	var rows [][]string
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue // skip malformed rows
		}

		cricinfoID := ""
		if cricinfoIdx < len(record) {
			cricinfoID = strings.TrimSpace(record[cricinfoIdx])
		}

		identifier := ""
		if identifierIdx < len(record) {
			identifier = strings.TrimSpace(record[identifierIdx])
		}

		name := ""
		if nameIdx < len(record) {
			name = strings.TrimSpace(record[nameIdx])
		}

		rows = append(rows, []string{identifier, name, cricinfoID})
	}

	return rows, nil
}

// loadExistingIDs reads the output CSV and returns a set of already-scraped cricinfo IDs.
func loadExistingIDs(path string) map[string]struct{} {
	existing := make(map[string]struct{})

	f, err := os.Open(path)
	if err != nil {
		return existing // file doesn't exist yet
	}
	defer f.Close()

	reader := csv.NewReader(f)
	header, err := reader.Read()
	if err != nil {
		return existing
	}

	cricinfoIdx := -1
	for i, col := range header {
		if col == "key_cricinfo" {
			cricinfoIdx = i
			break
		}
	}
	if cricinfoIdx == -1 {
		return existing
	}

	for {
		record, err := reader.Read()
		if err != nil {
			break
		}
		if cricinfoIdx < len(record) {
			existing[record[cricinfoIdx]] = struct{}{}
		}
	}

	return existing
}

// scrapePlayer fetches player attributes from the ESPN Core API with retry logic.
func scrapePlayer(client *http.Client, identifier, name, cricinfoID string) PlayerResult {
	result := PlayerResult{
		Identifier:  identifier,
		Name:        name,
		KeyCricinfo: cricinfoID,
	}

	url := fmt.Sprintf(espnCoreAPI, cricinfoID)

	const maxRetries = 3
	var lastErr string

	for attempt := range maxRetries {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt)) * time.Second // 2s, 4s, 8s
			time.Sleep(backoff)
		}

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			result.Error = fmt.Sprintf("creating request: %v", err)
			return result
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Sprintf("http request: %v", err)
			continue // retry on connection errors
		}

		if resp.StatusCode == 404 {
			resp.Body.Close()
			result.Error = "player not found (404)"
			return result // don't retry 404s
		}

		// Retry on transient server errors
		if resp.StatusCode == 429 || resp.StatusCode == 503 || resp.StatusCode == 500 || resp.StatusCode == 502 || resp.StatusCode == 504 {
			resp.Body.Close()
			lastErr = fmt.Sprintf("transient error: %d (attempt %d/%d)", resp.StatusCode, attempt+1, maxRetries)
			continue
		}

		if resp.StatusCode != 200 {
			resp.Body.Close()
			result.Error = fmt.Sprintf("unexpected status: %d", resp.StatusCode)
			return result
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Sprintf("reading body: %v", err)
			continue
		}

		var player ESPNPlayerResponse
		if err := json.Unmarshal(body, &player); err != nil {
			result.Error = fmt.Sprintf("parsing json: %v", err)
			return result // don't retry parse errors
		}

		result.FullName = player.FullName

		// Extract batting and bowling styles
		styles := player.Style
		if len(styles) == 0 {
			styles = player.Styles
		}
		for _, s := range styles {
			switch s.Type {
			case "batting":
				result.BattingStyle = s.Description
			case "bowling":
				result.BowlingStyle = s.Description
			}
		}

		// Extract playing role
		if player.Position != nil {
			result.PlayingRole = player.Position.Name
		}

		return result
	}

	// All retries exhausted
	result.Error = lastErr
	return result
}

// writeResults writes player results to a CSV file.
func writeResults(path string, results <-chan PlayerResult, writeHeader bool) error {
	flags := os.O_WRONLY | os.O_CREATE
	if writeHeader {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_APPEND
	}

	f, err := os.OpenFile(path, flags, 0644)
	if err != nil {
		return fmt.Errorf("opening output file: %w", err)
	}
	defer f.Close()

	writer := csv.NewWriter(f)
	defer writer.Flush()

	if writeHeader {
		if err := writer.Write([]string{
			"identifier", "name", "key_cricinfo", "full_name",
			"batting_style", "bowling_style", "playing_role", "error",
		}); err != nil {
			return fmt.Errorf("writing header: %w", err)
		}
	}

	for result := range results {
		if err := writer.Write([]string{
			result.Identifier,
			result.Name,
			result.KeyCricinfo,
			result.FullName,
			result.BattingStyle,
			result.BowlingStyle,
			result.PlayingRole,
			result.Error,
		}); err != nil {
			return fmt.Errorf("writing row: %w", err)
		}
	}

	return nil
}
