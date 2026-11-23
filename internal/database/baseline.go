package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

const undefinedFunction = "42883"

// RefreshLeaguePhaseBaseline recomputes league_phase_baseline, the per-league,
// per-season, per-phase scoring averages behind the API's league-adjusted
// strike rate and economy. It reports false when the database does not have
// the function yet (backend migration 000011 not applied).
func (db *DB) RefreshLeaguePhaseBaseline(ctx context.Context) (bool, error) {
	if _, err := db.pool.Exec(ctx, "SELECT refresh_league_phase_baseline()"); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == undefinedFunction {
			return false, nil
		}
		return false, fmt.Errorf("refresh league phase baseline: %w", err)
	}
	return true, nil
}
