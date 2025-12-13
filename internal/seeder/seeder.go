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

type Stats struct {
	MatchesProcessed   int64
	DeliveriesInserted int64
	InfoFilesProcessed int64
	Errors             []error
	mu                 sync.Mutex
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

	if skipExisting {
		exists, matchID, err := s.db.CheckLeagueExists(ctx, s.config.League)
		if err != nil {
			return fmt.Errorf("failed to check existing data: %w", err)
		}
		if exists {
			fmt.Printf("Skipping %s seeding: existing data found (match id %d)\n", s.config.League, matchID)
			return nil
		}
	}

	matchFiles, infoFiles, err := s.parser.GetMatchFiles()
	if err != nil {
		return fmt.Errorf("failed to get match files: %w", err)
	}

	fmt.Printf("Found %d matches and %d info files for %s\n", len(matchFiles), len(infoFiles), s.config.League)

	stats := &Stats{}

	fmt.Println("Processing match info files...")
	if err := s.processInfoFiles(ctx, infoFiles, stats); err != nil {
		return err
	}

	fmt.Println("Processing match files...")
	if err := s.processMatchFiles(ctx, matchFiles, stats); err != nil {
		return err
	}

	fmt.Printf("%s seed completed: %d matches and %d deliveries processed\n",
		s.config.League, stats.MatchesProcessed, stats.DeliveriesInserted)

	if len(stats.Errors) > 0 {
		fmt.Printf("Encountered %d errors during processing\n", len(stats.Errors))
		for _, e := range stats.Errors[:min(5, len(stats.Errors))] {
			fmt.Printf("  - %v\n", e)
		}
	}

	return nil
}

func (s *Seeder) processInfoFiles(ctx context.Context, files []string, stats *Stats) error {
	sem := make(chan struct{}, s.concurrency)
	var wg sync.WaitGroup

	for _, file := range files {
		wg.Add(1)
		sem <- struct{}{}

		go func(f string) {
			defer wg.Done()
			defer func() { <-sem }()

			if err := s.processInfoFile(ctx, f); err != nil {
				stats.mu.Lock()
				stats.Errors = append(stats.Errors, fmt.Errorf("info file %s: %w", f, err))
				stats.mu.Unlock()
				return
			}

			atomic.AddInt64(&stats.InfoFilesProcessed, 1)
		}(file)
	}

	wg.Wait()
	return nil
}

func (s *Seeder) processInfoFile(ctx context.Context, filePath string) error {
	matchInfo, teams, players, officials, registry, err := s.parser.ParseInfoFile(filePath)
	if err != nil {
		return err
	}

	if err := s.db.UpsertMatchInfo(ctx, matchInfo); err != nil {
		return fmt.Errorf("failed to upsert match info: %w", err)
	}

	if err := s.db.DeleteRelatedData(ctx, matchInfo.ID); err != nil {
		return fmt.Errorf("failed to delete related data: %w", err)
	}

	if err := s.db.InsertTeams(ctx, teams); err != nil {
		return fmt.Errorf("failed to insert teams: %w", err)
	}

	if err := s.db.InsertPlayers(ctx, players); err != nil {
		return fmt.Errorf("failed to insert players: %w", err)
	}

	if err := s.db.InsertOfficials(ctx, officials); err != nil {
		return fmt.Errorf("failed to insert officials: %w", err)
	}

	if err := s.db.InsertPersonRegistry(ctx, registry); err != nil {
		return fmt.Errorf("failed to insert person registry: %w", err)
	}

	return nil
}

func (s *Seeder) processMatchFiles(ctx context.Context, files []string, stats *Stats) error {
	sem := make(chan struct{}, s.concurrency)
	var wg sync.WaitGroup

	startTime := time.Now()
	total := len(files)

	for i, file := range files {
		wg.Add(1)
		sem <- struct{}{}

		go func(f string, idx int) {
			defer wg.Done()
			defer func() { <-sem }()

			deliveryCount, err := s.processMatchFile(ctx, f)
			if err != nil {
				stats.mu.Lock()
				stats.Errors = append(stats.Errors, fmt.Errorf("match file %s: %w", f, err))
				stats.mu.Unlock()
				return
			}

			atomic.AddInt64(&stats.MatchesProcessed, 1)
			atomic.AddInt64(&stats.DeliveriesInserted, deliveryCount)

			processed := atomic.LoadInt64(&stats.MatchesProcessed)
			if processed%10 == 0 || processed == int64(total) {
				elapsed := time.Since(startTime)
				rate := float64(processed) / elapsed.Seconds()
				fmt.Printf("Progress: %d/%d matches (%.1f matches/sec)\n", processed, total, rate)
			}
		}(file, i)
	}

	wg.Wait()
	return nil
}

func (s *Seeder) processMatchFile(ctx context.Context, filePath string) (int64, error) {
	match, deliveries, err := s.parser.ParseMatchFile(filePath)
	if err != nil {
		return 0, err
	}

	if err := s.db.UpsertMatch(ctx, match); err != nil {
		return 0, fmt.Errorf("failed to upsert match: %w", err)
	}

	count, err := s.db.InsertDeliveriesBulk(ctx, deliveries)
	if err != nil {
		count, err = s.db.InsertDeliveries(ctx, deliveries)
		if err != nil {
			return 0, fmt.Errorf("failed to insert deliveries: %w", err)
		}
	}

	return count, nil
}
