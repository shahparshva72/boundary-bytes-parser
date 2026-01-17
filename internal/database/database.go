package database

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shahparshva72/boundary-bytes-parser/internal/models"
)

type DB struct {
	pool *pgxpool.Pool
}

func NewDB(connectionString string) (*DB, error) {
	config, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", err)
	}

	// Disable prepared statement cache to avoid conflicts with concurrent connections
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

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

func (db *DB) UpsertMatch(ctx context.Context, match *models.Match) error {
	_, err := db.pool.Exec(ctx, `
		INSERT INTO wpl_match (match_id, league, season, start_date, venue)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (match_id) DO UPDATE SET
			league = EXCLUDED.league,
			season = EXCLUDED.season,
			start_date = EXCLUDED.start_date,
			venue = EXCLUDED.venue
	`, match.ID, match.League, match.Season, match.StartDate, match.Venue)

	return err
}

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

func (db *DB) InsertDeliveriesBulk(ctx context.Context, deliveries []models.Delivery) (int64, error) {
	if len(deliveries) == 0 {
		return 0, nil
	}

	rows := make([][]interface{}, len(deliveries))
	for i, d := range deliveries {
		rows[i] = []interface{}{
			d.MatchID, d.Innings, d.Ball, d.BattingTeam, d.BowlingTeam,
			d.Striker, d.NonStriker, d.Bowler, d.RunsOffBat, d.Extras,
			d.Wides, d.Noballs, d.Byes, d.Legbyes, d.Penalty,
			d.WicketType, d.PlayerDismissed, d.OtherWicketType, d.OtherPlayerDismissed,
		}
	}

	copyCount, err := db.pool.CopyFrom(
		ctx,
		pgx.Identifier{"wpl_delivery"},
		[]string{
			"match_id", "innings", "ball", "batting_team", "bowling_team",
			"striker", "non_striker", "bowler", "runs_off_bat", "extras",
			"wides", "noballs", "byes", "legbyes", "penalty",
			"wicket_type", "player_dismissed", "other_wicket_type", "other_player_dismissed",
		},
		pgx.CopyFromRows(rows),
	)

	return copyCount, err
}

func (db *DB) UpsertMatchInfo(ctx context.Context, info *models.MatchInfo) error {
	_, err := db.pool.Exec(ctx, `
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

	return err
}

func (db *DB) DeleteRelatedData(ctx context.Context, matchID int) error {
	tables := []string{"wpl_team", "wpl_player", "wpl_official", "wpl_person_registry"}

	for _, table := range tables {
		_, err := db.pool.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE match_id = $1", table), matchID)
		if err != nil {
			return fmt.Errorf("failed to delete from %s: %w", table, err)
		}
	}

	return nil
}

func (db *DB) InsertTeams(ctx context.Context, teams []models.Team) error {
	if len(teams) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, t := range teams {
		batch.Queue(`INSERT INTO wpl_team (match_id, team_name) VALUES ($1, $2)`, t.MatchID, t.TeamName)
	}

	br := db.pool.SendBatch(ctx, batch)
	defer br.Close()

	for range teams {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}

	return nil
}

func (db *DB) InsertPlayers(ctx context.Context, players []models.Player) error {
	if len(players) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, p := range players {
		batch.Queue(`INSERT INTO wpl_player (match_id, team_name, player_name) VALUES ($1, $2, $3)`,
			p.MatchID, p.TeamName, p.PlayerName)
	}

	br := db.pool.SendBatch(ctx, batch)
	defer br.Close()

	for range players {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}

	return nil
}

func (db *DB) InsertOfficials(ctx context.Context, officials []models.Official) error {
	if len(officials) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, o := range officials {
		batch.Queue(`INSERT INTO wpl_official (match_id, official_type, official_name) VALUES ($1, $2, $3)`,
			o.MatchID, o.OfficialType, o.OfficialName)
	}

	br := db.pool.SendBatch(ctx, batch)
	defer br.Close()

	for range officials {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}

	return nil
}

func (db *DB) InsertPersonRegistry(ctx context.Context, registry []models.PersonRegistry) error {
	if len(registry) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, r := range registry {
		batch.Queue(`INSERT INTO wpl_person_registry (match_id, person_name, registry_id) VALUES ($1, $2, $3)`,
			r.MatchID, r.PersonName, r.RegistryID)
	}

	br := db.pool.SendBatch(ctx, batch)
	defer br.Close()

	for range registry {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}

	return nil
}

func (db *DB) BeginTx(ctx context.Context) (pgx.Tx, error) {
	return db.pool.Begin(ctx)
}
