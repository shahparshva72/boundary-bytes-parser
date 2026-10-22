package seeder

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shahparshva72/boundary-bytes-parser/internal/database"
	"github.com/shahparshva72/boundary-bytes-parser/internal/leagues"
	"github.com/shahparshva72/boundary-bytes-parser/internal/models"
	"github.com/shahparshva72/boundary-bytes-parser/internal/parser"
	"github.com/shahparshva72/boundary-bytes-parser/internal/people"
)

type Seeder struct {
	db          *database.DB
	directory   string
	registry    *leagues.Registry
	only        map[string]bool
	people      people.Register
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

type eventKey struct {
	event  string
	gender string
}

// NewSeeder loads one Cricsheet csv2 directory. only limits loading to the
// given league codes; nil loads every league in the registry.
func NewSeeder(db *database.DB, directory string, registry *leagues.Registry, only map[string]bool, register people.Register, concurrency int) *Seeder {
	return &Seeder{
		db:          db,
		directory:   directory,
		registry:    registry,
		only:        only,
		people:      register,
		parser:      parser.NewCSVParser(directory),
		concurrency: concurrency,
	}
}

func (s *Seeder) Run(ctx context.Context, skipExisting bool) error {
	if _, err := os.Stat(s.directory); os.IsNotExist(err) {
		fmt.Printf("Directory %s does not exist, skipping\n", s.directory)
		return nil
	}

	matchFiles, infoFiles, err := s.parser.GetMatchFiles()
	if err != nil {
		return fmt.Errorf("failed to get match files: %w", err)
	}

	if skipExisting {
		existingIDs, err := s.db.GetExistingMatchIDs(ctx)
		if err != nil {
			return fmt.Errorf("failed to get existing match IDs: %w", err)
		}

		if len(existingIDs) > 0 {
			before := len(matchFiles)
			matchFiles = filterNewFiles(matchFiles, existingIDs, false)
			infoFiles = filterNewFiles(infoFiles, existingIDs, true)
			fmt.Printf("Skipping %d already-loaded matches in %s\n", before-len(matchFiles), s.directory)
		}
	}

	fmt.Printf("Found %d new matches and %d new info files in %s\n", len(matchFiles), len(infoFiles), s.directory)

	if len(matchFiles) == 0 && len(infoFiles) == 0 {
		fmt.Printf("No new data to process in %s\n", s.directory)
		return nil
	}

	totalStart := time.Now()

	// ── Phase 1: Parse all files concurrently (CPU-bound, no DB) ──
	// Info files come first: they carry the event and gender that decide
	// each match's league, and the registry IDs used to make names unique.
	fmt.Println("Phase 1: Parsing all CSV files...")
	parseStart := time.Now()

	parsedInfos, infoParseErrors := s.parseAllInfoFiles(infoFiles)
	parsedInfos, leagueByMatch := s.resolveLeagues(parsedInfos)
	matchFiles = filterResolvedFiles(matchFiles, leagueByMatch)
	parsedMatches, matchParseErrors := s.parseAllMatchFiles(matchFiles)

	for i := range parsedMatches {
		parsedMatches[i].match.League = leagueByMatch[parsedMatches[i].match.ID]
	}
	s.applyUniqueNames(parsedInfos, parsedMatches)

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

	perLeague := make(map[string]int)
	for _, m := range parsedMatches {
		perLeague[m.match.League]++
	}
	codes := make([]string, 0, len(perLeague))
	for code := range perLeague {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		fmt.Printf("  %s: %d matches\n", code, perLeague[code])
	}

	rate := float64(len(parsedMatches)) / totalDuration.Seconds()
	fmt.Printf("%s seed completed: %d matches, %d deliveries in %s (%.0f matches/sec)\n",
		s.directory, len(parsedMatches), deliveryCount,
		totalDuration.Round(time.Millisecond), rate)

	return nil
}

// resolveLeagues assigns each match its league from Cricsheet's (event,
// gender) and drops matches whose event isn't in the registry or whose league
// wasn't requested. Unmapped events are reported, never guessed.
func (s *Seeder) resolveLeagues(infos []parsedInfo) ([]parsedInfo, map[int]string) {
	leagueByMatch := make(map[int]string, len(infos))
	unmapped := make(map[eventKey]int)
	resolved := infos[:0]

	for _, info := range infos {
		l, ok := s.registry.Resolve(info.matchInfo.Event, info.matchInfo.Gender)
		if !ok {
			unmapped[eventKey{info.matchInfo.Event, info.matchInfo.Gender}]++
			continue
		}
		if s.only != nil && !s.only[l.Code] {
			continue
		}
		info.matchInfo.League = l.Code
		leagueByMatch[info.matchInfo.ID] = l.Code
		resolved = append(resolved, info)
	}

	if len(unmapped) > 0 {
		keys := make([]eventKey, 0, len(unmapped))
		total := 0
		for k, n := range unmapped {
			keys = append(keys, k)
			total += n
		}
		sort.Slice(keys, func(i, j int) bool { return unmapped[keys[i]] > unmapped[keys[j]] })

		// A league folder should have none; Cricsheet's mixed "recently added"
		// zips also carry internationals and other formats, so cap the list.
		const maxListed = 15
		fmt.Printf("  WARNING: skipped %d matches with events not in leagues.json:\n", total)
		for _, k := range keys[:min(maxListed, len(keys))] {
			fmt.Printf("    - %q (%s): %d matches\n", k.event, k.gender, unmapped[k])
		}
		if len(keys) > maxListed {
			fmt.Printf("    ... and %d more events\n", len(keys)-maxListed)
		}
	}

	return resolved, leagueByMatch
}

// applyUniqueNames replaces each player's Cricsheet name with the people
// register's unique_name, using the match's own registry to identify people.
// unique_name equals name for almost everyone, so only clashing names change.
//
// A few older files name a player differently in the info file ("Carolyn
// Esterhuizen") and the ball-by-ball file ("C Esterhuizen"), so the register's
// short name is mapped too, as long as no one else in the match shares it.
func (s *Seeder) applyUniqueNames(infos []parsedInfo, matches []parsedMatch) {
	renamesByMatch := make(map[int]map[string]string)
	missing := make(map[string]bool)

	for i := range infos {
		info := &infos[i]
		renames := make(map[string]string)
		registered := make(map[string]bool, len(info.registry))
		shortNames := make(map[string][]string)
		for _, r := range info.registry {
			registered[r.PersonName] = true
		}
		for j := range info.registry {
			r := &info.registry[j]
			person, ok := s.people[r.RegistryID]
			if !ok {
				missing[r.RegistryID] = true
				continue
			}
			if person.Name != r.PersonName && !registered[person.Name] {
				shortNames[person.Name] = append(shortNames[person.Name], person.UniqueName)
			}
			if person.UniqueName != r.PersonName {
				renames[r.PersonName] = person.UniqueName
				r.PersonName = person.UniqueName
			}
		}
		for short, uniques := range shortNames {
			if len(uniques) == 1 && short != uniques[0] {
				renames[short] = uniques[0]
			}
		}
		if len(renames) == 0 {
			continue
		}
		renamesByMatch[info.matchInfo.ID] = renames

		for j := range info.players {
			rename(&info.players[j].PlayerName, renames)
		}
		if info.matchInfo.PlayerOfMatch != nil {
			rename(info.matchInfo.PlayerOfMatch, renames)
		}
	}

	renamed := 0
	for _, m := range matches {
		renames, ok := renamesByMatch[m.match.ID]
		if !ok {
			continue
		}
		renamed++
		for j := range m.deliveries {
			d := &m.deliveries[j]
			rename(&d.Striker, renames)
			rename(&d.NonStriker, renames)
			rename(&d.Bowler, renames)
			if d.PlayerDismissed != nil {
				rename(d.PlayerDismissed, renames)
			}
			if d.OtherPlayerDismissed != nil {
				rename(d.OtherPlayerDismissed, renames)
			}
		}
	}

	if renamed > 0 {
		fmt.Printf("  Applied unique player names in %d matches\n", renamed)
	}
	if len(missing) > 0 {
		fmt.Printf("  WARNING: %d registry IDs not in people.csv (names kept as-is); download a fresh register\n", len(missing))
	}
}

func rename(name *string, renames map[string]string) {
	if unique, ok := renames[*name]; ok {
		*name = unique
	}
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

	// Re-seeded matches would otherwise get a second copy of every delivery.
	matchIDs := make([]int, len(matches))
	for i, m := range matches {
		matchIDs[i] = m.ID
	}
	if err := s.db.DeleteMatchDeliveries(ctx, matchIDs); err != nil {
		return 0, fmt.Errorf("failed to clear deliveries for re-seed: %w", err)
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

// filterResolvedFiles keeps match files whose info file resolved to a league.
// A match file without an info file can't be assigned a league, so it's skipped.
func filterResolvedFiles(files []string, leagueByMatch map[int]string) []string {
	var kept []string
	for _, f := range files {
		matchID, err := parser.ExtractMatchID(f, false)
		if err != nil {
			continue
		}
		if _, ok := leagueByMatch[matchID]; ok {
			kept = append(kept, f)
		}
	}
	return kept
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
