package database

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shahparshva72/boundary-bytes-parser/internal/models"
)

type DB struct {
	pool *pgxpool.Pool
}

func NewDB(connectionString string, maxConns ...int32) (*DB, error) {
	config, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", err)
	}

	// Disable prepared statement cache to avoid conflicts with concurrent connections
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	// Tune pool size: use explicit value or default to max(numCPU, 10)
	if len(maxConns) > 0 && maxConns[0] > 0 {
		config.MaxConns = maxConns[0]
	} else {
		poolSize := int32(runtime.NumCPU())
		if poolSize < 10 {
			poolSize = 10
		}
		config.MaxConns = poolSize
	}

	// Session-level tuning for bulk loading:
	// - synchronous_commit=off: don't wait for WAL flush (safe for seeder)
	// - work_mem: larger sort buffers for index maintenance during COPY
	// - maintenance_work_mem: larger buffers for index builds
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `
			SET synchronous_commit = off;
			SET work_mem = '128MB';
			SET maintenance_work_mem = '256MB'
		`)
		return err
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	if err := pool.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &DB{pool: pool}, nil
}

func (db *DB) Close() {
	db.pool.Close()
}

func (db *DB) CheckLeagueExists(ctx context.Context, league string) (bool, int, error) {
	var matchID int
	err := db.pool.QueryRow(ctx, `
		SELECT match_id FROM wpl_match WHERE league = $1 LIMIT 1
	`, league).Scan(&matchID)

	if err == pgx.ErrNoRows {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	return true, matchID, nil
}

func (db *DB) GetExistingMatchIDs(ctx context.Context, league string) (map[int]bool, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT match_id FROM wpl_match WHERE league = $1
	`, league)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	existing := make(map[int]bool)
	for rows.Next() {
		var matchID int
		if err := rows.Scan(&matchID); err != nil {
			return nil, err
		}
		existing[matchID] = true
	}

	return existing, rows.Err()
}

// BulkUpsertMatches upserts all matches in a single pgx.Batch round-trip.
func (db *DB) BulkUpsertMatches(ctx context.Context, matches []*models.Match) error {
	if len(matches) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, m := range matches {
		batch.Queue(`
			INSERT INTO wpl_match (match_id, league, season, start_date, venue)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (match_id) DO UPDATE SET
				league = EXCLUDED.league,
				season = EXCLUDED.season,
				start_date = EXCLUDED.start_date,
				venue = EXCLUDED.venue
		`, m.ID, m.League, m.Season, m.StartDate, m.Venue)
	}

	br := db.pool.SendBatch(ctx, batch)
	defer br.Close()

	for range matches {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("failed to upsert match: %w", err)
		}
	}

	return nil
}

// InsertDeliveriesBulk inserts ALL deliveries via a single CopyFrom call.
func (db *DB) InsertDeliveriesBulk(ctx context.Context, deliveries []models.Delivery) (int64, error) {
	if len(deliveries) == 0 {
		return 0, nil
	}

	rows := deliveriesToRows(deliveries)

	copyCount, err := db.pool.CopyFrom(
		ctx,
		pgx.Identifier{"wpl_delivery"},
		deliveryColumns,
		pgx.CopyFromRows(rows),
	)

	return copyCount, err
}

// InsertDeliveriesParallel splits deliveries into N chunks and COPYs them
// in parallel across separate pool connections for maximum throughput.
func (db *DB) InsertDeliveriesParallel(ctx context.Context, deliveries []models.Delivery, workers int) (int64, error) {
	if len(deliveries) == 0 {
		return 0, nil
	}
	if workers <= 1 {
		return db.InsertDeliveriesBulk(ctx, deliveries)
	}

	chunkSize := (len(deliveries) + workers - 1) / workers
	var totalInserted int64
	var firstErr error
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < len(deliveries); i += chunkSize {
		end := i + chunkSize
		if end > len(deliveries) {
			end = len(deliveries)
		}
		chunk := deliveries[i:end]

		wg.Add(1)
		go func(chunk []models.Delivery) {
			defer wg.Done()

			rows := deliveriesToRows(chunk)
			count, err := db.pool.CopyFrom(
				ctx,
				pgx.Identifier{"wpl_delivery"},
				deliveryColumns,
				pgx.CopyFromRows(rows),
			)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			atomic.AddInt64(&totalInserted, count)
		}(chunk)
	}

	wg.Wait()
	return totalInserted, firstErr
}

var deliveryColumns = []string{
	"match_id", "innings", "ball", "batting_team", "bowling_team",
	"striker", "non_striker", "bowler", "runs_off_bat", "extras",
	"wides", "noballs", "byes", "legbyes", "penalty",
	"wicket_type", "player_dismissed", "other_wicket_type", "other_player_dismissed",
}

func deliveriesToRows(deliveries []models.Delivery) [][]interface{} {
	rows := make([][]interface{}, len(deliveries))
	for i, d := range deliveries {
		rows[i] = []interface{}{
			d.MatchID, d.Innings, d.Ball, d.BattingTeam, d.BowlingTeam,
			d.Striker, d.NonStriker, d.Bowler, d.RunsOffBat, d.Extras,
			d.Wides, d.Noballs, d.Byes, d.Legbyes, d.Penalty,
			d.WicketType, d.PlayerDismissed, d.OtherWicketType, d.OtherPlayerDismissed,
		}
	}
	return rows
}

// InsertDeliveries inserts deliveries using a pgx.Batch (fallback for conflicts).
func (db *DB) InsertDeliveries(ctx context.Context, deliveries []models.Delivery) (int64, error) {
	if len(deliveries) == 0 {
		return 0, nil
	}

	batch := &pgx.Batch{}

	for _, d := range deliveries {
		batch.Queue(`
			INSERT INTO wpl_delivery (
				match_id, innings, ball, batting_team, bowling_team,
				striker, non_striker, bowler, runs_off_bat, extras,
				wides, noballs, byes, legbyes, penalty,
				wicket_type, player_dismissed, other_wicket_type, other_player_dismissed
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
		`,
			d.MatchID, d.Innings, d.Ball, d.BattingTeam, d.BowlingTeam,
			d.Striker, d.NonStriker, d.Bowler, d.RunsOffBat, d.Extras,
			d.Wides, d.Noballs, d.Byes, d.Legbyes, d.Penalty,
			d.WicketType, d.PlayerDismissed, d.OtherWicketType, d.OtherPlayerDismissed,
		)
	}

	br := db.pool.SendBatch(ctx, batch)
	defer br.Close()

	var inserted int64
	for range deliveries {
		ct, err := br.Exec()
		if err != nil {
			if !strings.Contains(err.Error(), "duplicate key") {
				return inserted, err
			}
		} else {
			inserted += ct.RowsAffected()
		}
	}

	return inserted, nil
}

// BulkUpsertMatchInfos upserts all match info records in a single batch.
func (db *DB) BulkUpsertMatchInfos(ctx context.Context, infos []*models.MatchInfo) error {
	if len(infos) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, info := range infos {
		batch.Queue(`
			INSERT INTO wpl_match_info (
				match_id, league, version, balls_per_over, gender, season, date,
				event, match_number, venue, city, toss_winner, toss_decision,
				player_of_match, winner, winner_runs, winner_wickets
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
			ON CONFLICT (match_id) DO UPDATE SET
				league = EXCLUDED.league,
				version = EXCLUDED.version,
				balls_per_over = EXCLUDED.balls_per_over,
				gender = EXCLUDED.gender,
				season = EXCLUDED.season,
				date = EXCLUDED.date,
				event = EXCLUDED.event,
				match_number = EXCLUDED.match_number,
				venue = EXCLUDED.venue,
				city = EXCLUDED.city,
				toss_winner = EXCLUDED.toss_winner,
				toss_decision = EXCLUDED.toss_decision,
				player_of_match = EXCLUDED.player_of_match,
				winner = EXCLUDED.winner,
				winner_runs = EXCLUDED.winner_runs,
				winner_wickets = EXCLUDED.winner_wickets
		`, info.ID, info.League, info.Version, info.BallsPerOver, info.Gender, info.Season, info.Date,
			info.Event, info.MatchNumber, info.Venue, info.City, info.TossWinner, info.TossDecision,
			info.PlayerOfMatch, info.Winner, info.WinnerRuns, info.WinnerWickets)
	}

	br := db.pool.SendBatch(ctx, batch)
	defer br.Close()

	for range infos {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("failed to upsert match info: %w", err)
		}
	}

	return nil
}

// BulkDeleteRelatedData deletes related data for all match IDs using ANY().
// 4 statements total instead of 4×N individual DELETEs.
func (db *DB) BulkDeleteRelatedData(ctx context.Context, matchIDs []int) error {
	if len(matchIDs) == 0 {
		return nil
	}

	tables := []string{"wpl_team", "wpl_player", "wpl_official", "wpl_person_registry"}
	for _, table := range tables {
		_, err := db.pool.Exec(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE match_id = ANY($1)", table), matchIDs)
		if err != nil {
			return fmt.Errorf("failed to delete from %s: %w", table, err)
		}
	}

	return nil
}

// InsertTeamsBulk inserts all teams via CopyFrom.
func (db *DB) InsertTeamsBulk(ctx context.Context, teams []models.Team) (int64, error) {
	if len(teams) == 0 {
		return 0, nil
	}

	rows := make([][]interface{}, len(teams))
	for i, t := range teams {
		rows[i] = []interface{}{t.MatchID, t.TeamName}
	}

	return db.pool.CopyFrom(ctx,
		pgx.Identifier{"wpl_team"},
		[]string{"match_id", "team_name"},
		pgx.CopyFromRows(rows),
	)
}

// InsertPlayersBulk inserts all players via CopyFrom.
func (db *DB) InsertPlayersBulk(ctx context.Context, players []models.Player) (int64, error) {
	if len(players) == 0 {
		return 0, nil
	}

	rows := make([][]interface{}, len(players))
	for i, p := range players {
		rows[i] = []interface{}{p.MatchID, p.TeamName, p.PlayerName}
	}

	return db.pool.CopyFrom(ctx,
		pgx.Identifier{"wpl_player"},
		[]string{"match_id", "team_name", "player_name"},
		pgx.CopyFromRows(rows),
	)
}

// InsertOfficialsBulk inserts all officials via CopyFrom.
func (db *DB) InsertOfficialsBulk(ctx context.Context, officials []models.Official) (int64, error) {
	if len(officials) == 0 {
		return 0, nil
	}

	rows := make([][]interface{}, len(officials))
	for i, o := range officials {
		rows[i] = []interface{}{o.MatchID, o.OfficialType, o.OfficialName}
	}

	return db.pool.CopyFrom(ctx,
		pgx.Identifier{"wpl_official"},
		[]string{"match_id", "official_type", "official_name"},
		pgx.CopyFromRows(rows),
	)
}

// InsertPersonRegistryBulk inserts all person registry entries via CopyFrom.
func (db *DB) InsertPersonRegistryBulk(ctx context.Context, registry []models.PersonRegistry) (int64, error) {
	if len(registry) == 0 {
		return 0, nil
	}

	rows := make([][]interface{}, len(registry))
	for i, r := range registry {
		rows[i] = []interface{}{r.MatchID, r.PersonName, r.RegistryID}
	}

	return db.pool.CopyFrom(ctx,
		pgx.Identifier{"wpl_person_registry"},
		[]string{"match_id", "person_name", "registry_id"},
		pgx.CopyFromRows(rows),
	)
}

func (db *DB) BeginTx(ctx context.Context) (pgx.Tx, error) {
	return db.pool.Begin(ctx)
}

func (db *DB) GetPlayerStyleCount(ctx context.Context) (int, error) {
	var count int
	err := db.pool.QueryRow(ctx, `SELECT COUNT(*) FROM player_style`).Scan(&count)
	return count, err
}

func (db *DB) UpsertPlayerStylesBulk(ctx context.Context, styles []models.PlayerStyle) (int64, error) {
	if len(styles) == 0 {
		return 0, nil
	}

	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Truncate for a clean reload since this is a lookup table
	_, err = tx.Exec(ctx, `TRUNCATE TABLE player_style`)
	if err != nil {
		return 0, fmt.Errorf("failed to truncate player_style: %w", err)
	}

	rows := make([][]interface{}, len(styles))
	for i, s := range styles {
		rows[i] = []interface{}{
			s.Identifier, s.KeyCricinfo, s.Name, s.FullName,
			s.BattingHand, s.BowlingHand, s.BowlingType, s.BowlingSubType,
			s.PlayingRole, s.PlayingRoleDetail, s.BattingStyleRaw, s.BowlingStyleRaw,
		}
	}

	copyCount, err := tx.CopyFrom(
		ctx,
		pgx.Identifier{"player_style"},
		[]string{
			"identifier", "key_cricinfo", "name", "full_name",
			"batting_hand", "bowling_hand", "bowling_type", "bowling_sub_type",
			"playing_role", "playing_role_detail", "batting_style_raw", "bowling_style_raw",
		},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to copy player styles: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return copyCount, nil
}
