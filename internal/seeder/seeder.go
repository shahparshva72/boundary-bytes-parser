package seeder

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shahparshva72/boundary-bytes-parser/internal/database"
	"github.com/shahparshva72/boundary-bytes-parser/internal/models"
	"github.com/shahparshva72/boundary-bytes-parser/internal/parser"
)

type Seeder struct {
	db          *database.DB
	config      models.LeagueConfig
	parser      *parser.CSVParser
	concurrency int
}

// parsedMatch holds all data parsed from a single match CSV file.
type parsedMatch struct {
	match      *models.Match
	deliveries []models.Delivery
}

// parsedInfo holds all data parsed from a single info CSV file.
type parsedInfo struct {
	matchInfo *models.MatchInfo
	teams     []models.Team
	players   []models.Player
	officials []models.Official
	registry  []models.PersonRegistry
}

func NewSeeder(db *database.DB, config models.LeagueConfig, concurrency int) *Seeder {
	return &Seeder{
		db:          db,
		config:      config,
		parser:      parser.NewCSVParser(config),
		concurrency: concurrency,
	}
}

func (s *Seeder) Run(ctx context.Context, skipExisting bool) error {
	if _, err := os.Stat(s.config.CSVDirectory); os.IsNotExist(err) {
		fmt.Printf("Directory %s does not exist, skipping %s\n", s.config.CSVDirectory, s.config.League)
		return nil
	}

	matchFiles, infoFiles, err := s.parser.GetMatchFiles()
	if err != nil {
		return fmt.Errorf("failed to get match files: %w", err)
	}

	if skipExisting {
		existingIDs, err := s.db.GetExistingMatchIDs(ctx, s.config.League)
		if err != nil {
			return fmt.Errorf("failed to get existing match IDs: %w", err)
		}

		if len(existingIDs) > 0 {
			matchFiles = filterNewFiles(matchFiles, existingIDs, false)
			infoFiles = filterNewFiles(infoFiles, existingIDs, true)
			fmt.Printf("Skipping %d existing matches for %s\n", len(existingIDs), s.config.League)
		}
	}

	fmt.Printf("Found %d new matches and %d new info files for %s\n", len(matchFiles), len(infoFiles), s.config.League)

	if len(matchFiles) == 0 && len(infoFiles) == 0 {
		fmt.Printf("No new data to process for %s\n", s.config.League)
		return nil
	}

	totalStart := time.Now()

	// ── Phase 1: Parse all files concurrently (CPU-bound, no DB) ──
	fmt.Println("Phase 1: Parsing all CSV files...")
	parseStart := time.Now()

	parsedMatches, matchParseErrors := s.parseAllMatchFiles(matchFiles)
	parsedInfos, infoParseErrors := s.parseAllInfoFiles(infoFiles)

	parseDuration := time.Since(parseStart)
	fmt.Printf("  Parsed %d matches + %d info files in %s\n",
		len(parsedMatches), len(parsedInfos), parseDuration.Round(time.Millisecond))

	if len(matchParseErrors) > 0 {
		fmt.Printf("  %d match parse errors\n", len(matchParseErrors))
		for _, e := range matchParseErrors[:min(3, len(matchParseErrors))] {
			fmt.Printf("    - %v\n", e)
		}
	}
	if len(infoParseErrors) > 0 {
		fmt.Printf("  %d info parse errors\n", len(infoParseErrors))
		for _, e := range infoParseErrors[:min(3, len(infoParseErrors))] {
			fmt.Printf("    - %v\n", e)
		}
	}

	// ── Phase 2: Bulk write to database (I/O-bound, minimal round-trips) ──
	fmt.Println("Phase 2: Bulk writing to database...")
	writeStart := time.Now()

	if err := s.bulkWriteInfoData(ctx, parsedInfos); err != nil {
		return fmt.Errorf("failed to write info data: %w", err)
	}

	deliveryCount, err := s.bulkWriteMatchData(ctx, parsedMatches)
	if err != nil {
		return fmt.Errorf("failed to write match data: %w", err)
	}

	writeDuration := time.Since(writeStart)
	totalDuration := time.Since(totalStart)

	fmt.Printf("  Wrote %d matches + %d deliveries in %s\n",
		len(parsedMatches), deliveryCount, writeDuration.Round(time.Millisecond))

	rate := float64(len(parsedMatches)) / totalDuration.Seconds()
	fmt.Printf("%s seed completed: %d matches, %d deliveries in %s (%.0f matches/sec)\n",
		s.config.League, len(parsedMatches), deliveryCount,
		totalDuration.Round(time.Millisecond), rate)

	return nil
}

// parseAllMatchFiles parses all match CSV files concurrently.
func (s *Seeder) parseAllMatchFiles(files []string) ([]parsedMatch, []error) {
	if len(files) == 0 {
		return nil, nil
	}

	type result struct {
		data parsedMatch
		err  error
	}

	results := make([]result, len(files))
	sem := make(chan struct{}, s.concurrency)
	var wg sync.WaitGroup
	var parsed int64

	for i, file := range files {
		wg.Add(1)
		sem <- struct{}{}

		go func(idx int, f string) {
			defer wg.Done()
			defer func() { <-sem }()

			match, deliveries, err := s.parser.ParseMatchFile(f)
			if err != nil {
				results[idx] = result{err: fmt.Errorf("match file %s: %w", f, err)}
				return
			}

			results[idx] = result{data: parsedMatch{match: match, deliveries: deliveries}}
			count := atomic.AddInt64(&parsed, 1)
			if count%100 == 0 {
				fmt.Printf("  Parsed %d/%d match files...\n", count, len(files))
			}
		}(i, file)
	}

	wg.Wait()

	var data []parsedMatch
	var errs []error
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r.err)
		} else if r.data.match != nil {
			data = append(data, r.data)
		}
	}

	return data, errs
}

// parseAllInfoFiles parses all info CSV files concurrently.
func (s *Seeder) parseAllInfoFiles(files []string) ([]parsedInfo, []error) {
	if len(files) == 0 {
		return nil, nil
	}

	type result struct {
		data parsedInfo
		err  error
	}

	results := make([]result, len(files))
	sem := make(chan struct{}, s.concurrency)
	var wg sync.WaitGroup

	for i, file := range files {
		wg.Add(1)
		sem <- struct{}{}

		go func(idx int, f string) {
			defer wg.Done()
			defer func() { <-sem }()

			matchInfo, teams, players, officials, registry, err := s.parser.ParseInfoFile(f)
			if err != nil {
				results[idx] = result{err: fmt.Errorf("info file %s: %w", f, err)}
				return
			}

			results[idx] = result{data: parsedInfo{
				matchInfo: matchInfo,
				teams:     teams,
				players:   players,
				officials: officials,
				registry:  registry,
			}}
		}(i, file)
	}

	wg.Wait()

	var data []parsedInfo
	var errs []error
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r.err)
		} else if r.data.matchInfo != nil {
			data = append(data, r.data)
		}
	}

	return data, errs
}

// bulkWriteMatchData writes all matches and deliveries in minimal DB round-trips.
func (s *Seeder) bulkWriteMatchData(ctx context.Context, data []parsedMatch) (int64, error) {
	if len(data) == 0 {
		return 0, nil
	}

	// Collect all matches for bulk upsert
	matches := make([]*models.Match, len(data))
	totalDeliveries := 0
	for i, d := range data {
		matches[i] = d.match
		totalDeliveries += len(d.deliveries)
	}

	// Bulk upsert all matches (single batch round-trip)
	fmt.Printf("  Upserting %d matches...\n", len(matches))
	if err := s.db.BulkUpsertMatches(ctx, matches); err != nil {
		return 0, fmt.Errorf("failed to bulk upsert matches: %w", err)
	}

	// Flatten all deliveries into one slice
	allDeliveries := make([]models.Delivery, 0, totalDeliveries)
	for _, d := range data {
		allDeliveries = append(allDeliveries, d.deliveries...)
	}

	// Parallel COPY across multiple connections
	fmt.Printf("  Inserting %d deliveries via parallel COPY (%d workers)...\n", len(allDeliveries), s.concurrency)
	count, err := s.db.InsertDeliveriesParallel(ctx, allDeliveries, s.concurrency)
	if err != nil {
		// Fallback: insert in per-match batches to handle conflicts
		fmt.Println("  Parallel COPY failed, falling back to batch inserts...")
		var totalInserted int64
		for _, d := range data {
			n, batchErr := s.db.InsertDeliveries(ctx, d.deliveries)
			if batchErr != nil {
				return totalInserted, fmt.Errorf("failed to insert deliveries for match %d: %w", d.match.ID, batchErr)
			}
			totalInserted += n
		}
		return totalInserted, nil
	}

	return count, nil
}

// bulkWriteInfoData writes all info-file data (match_info, teams, players, officials, registry)
// using bulk operations.
func (s *Seeder) bulkWriteInfoData(ctx context.Context, data []parsedInfo) error {
	if len(data) == 0 {
		return nil
	}

	// Collect all data
	var allInfos []*models.MatchInfo
	var allTeams []models.Team
	var allPlayers []models.Player
	var allOfficials []models.Official
	var allRegistry []models.PersonRegistry
	var matchIDs []int

	for _, d := range data {
		allInfos = append(allInfos, d.matchInfo)
		allTeams = append(allTeams, d.teams...)
		allPlayers = append(allPlayers, d.players...)
		allOfficials = append(allOfficials, d.officials...)
		allRegistry = append(allRegistry, d.registry...)
		matchIDs = append(matchIDs, d.matchInfo.ID)
	}

	// Bulk upsert all match infos
	fmt.Printf("  Upserting %d match infos...\n", len(allInfos))
	if err := s.db.BulkUpsertMatchInfos(ctx, allInfos); err != nil {
		return fmt.Errorf("failed to bulk upsert match infos: %w", err)
	}

	// Delete related data for all matches
	if err := s.db.BulkDeleteRelatedData(ctx, matchIDs); err != nil {
		return fmt.Errorf("failed to delete related data: %w", err)
	}

	// Bulk insert all related data via CopyFrom
	if _, err := s.db.InsertTeamsBulk(ctx, allTeams); err != nil {
		return fmt.Errorf("failed to bulk insert teams: %w", err)
	}
	if _, err := s.db.InsertPlayersBulk(ctx, allPlayers); err != nil {
		return fmt.Errorf("failed to bulk insert players: %w", err)
	}
	if _, err := s.db.InsertOfficialsBulk(ctx, allOfficials); err != nil {
		return fmt.Errorf("failed to bulk insert officials: %w", err)
	}
	if _, err := s.db.InsertPersonRegistryBulk(ctx, allRegistry); err != nil {
		return fmt.Errorf("failed to bulk insert person registry: %w", err)
	}

	return nil
}

func filterNewFiles(files []string, existingIDs map[int]bool, isInfo bool) []string {
	var newFiles []string
	for _, f := range files {
		matchID, err := parser.ExtractMatchID(f, isInfo)
		if err != nil {
			continue
		}
		if !existingIDs[matchID] {
			newFiles = append(newFiles, f)
		}
	}
	return newFiles
}
