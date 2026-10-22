package database

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// NameSyncResult reports how many rows SyncUniqueNames rewrote per table.
type NameSyncResult struct {
	Renames int64
	Rows    map[string]int64
}

// nameColumns lists every column that stores a player's Cricsheet name.
// wpl_person_registry is handled last because the rename list is built from it.
var nameColumns = []struct{ table, column string }{
	{"wpl_delivery", "striker"},
	{"wpl_delivery", "non_striker"},
	{"wpl_delivery", "bowler"},
	{"wpl_delivery", "player_dismissed"},
	{"wpl_delivery", "other_player_dismissed"},
	{"wpl_player", "player_name"},
	{"wpl_batting_position", "player_name"},
	{"wpl_match_info", "player_of_match"},
}

// SyncUniqueNames rewrites stored player names to Cricsheet's unique_name for
// each registry ID. Names are unique within a match, so every rename is keyed
// on (match_id, old name). It is idempotent: once names match the register,
// it changes nothing. Use it for the one-off backfill and for the periodic
// check that picks up Cricsheet renaming someone.
func (db *DB) SyncUniqueNames(ctx context.Context, uniqueNames map[string]string) (NameSyncResult, error) {
	result := NameSyncResult{Rows: make(map[string]int64)}

	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin name sync: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE people_names (
			registry_id TEXT PRIMARY KEY,
			unique_name TEXT NOT NULL
		) ON COMMIT DROP
	`); err != nil {
		return result, fmt.Errorf("create people_names: %w", err)
	}

	rows := make([][]interface{}, 0, len(uniqueNames))
	for id, name := range uniqueNames {
		rows = append(rows, []interface{}{id, name})
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"people_names"},
		[]string{"registry_id", "unique_name"}, pgx.CopyFromRows(rows)); err != nil {
		return result, fmt.Errorf("copy people_names: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE name_fix ON COMMIT DROP AS
		SELECT DISTINCT r.match_id, r.person_name AS old_name, p.unique_name AS new_name
		FROM wpl_person_registry r
		JOIN people_names p ON p.registry_id = r.registry_id
		WHERE r.person_name <> p.unique_name
	`); err != nil {
		return result, fmt.Errorf("build name_fix: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		CREATE INDEX ON name_fix (match_id, old_name);
		ANALYZE name_fix
	`); err != nil {
		return result, fmt.Errorf("index name_fix: %w", err)
	}

	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM name_fix`).Scan(&result.Renames); err != nil {
		return result, fmt.Errorf("count name_fix: %w", err)
	}
	if result.Renames == 0 {
		return result, tx.Commit(ctx)
	}

	for _, c := range nameColumns {
		tag, err := tx.Exec(ctx, fmt.Sprintf(`
			UPDATE %[1]s t SET %[2]s = f.new_name
			FROM name_fix f
			WHERE t.match_id = f.match_id AND t.%[2]s = f.old_name
		`, c.table, c.column))
		if err != nil {
			return result, fmt.Errorf("rename %s.%s: %w", c.table, c.column, err)
		}
		result.Rows[c.table+"."+c.column] = tag.RowsAffected()
	}

	tag, err := tx.Exec(ctx, `
		UPDATE wpl_person_registry r SET person_name = f.new_name
		FROM name_fix f
		WHERE r.match_id = f.match_id AND r.person_name = f.old_name
	`)
	if err != nil {
		return result, fmt.Errorf("rename wpl_person_registry.person_name: %w", err)
	}
	result.Rows["wpl_person_registry.person_name"] = tag.RowsAffected()

	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit name sync: %w", err)
	}

	return result, nil
}

// AssignLegacySlugRegistryIDs links player_slug rows created before
// player_slug.registry_id existed. A name shared by several people goes to the
// person with the most matches under it, so the existing URL stays with the
// player it most likely pointed at. Run it before SyncUniqueNames renames
// anyone, while names still match the old slugs.
func (db *DB) AssignLegacySlugRegistryIDs(ctx context.Context) (int64, error) {
	// A person can also appear under two names, so each registry ID is given
	// to at most one row: the name it played the most matches under.
	tag, err := db.pool.Exec(ctx, `
		WITH counts AS (
			SELECT person_name, registry_id, COUNT(*) AS matches
			FROM wpl_person_registry
			GROUP BY person_name, registry_id
		),
		name_owner AS (
			SELECT DISTINCT ON (person_name) person_name, registry_id, matches
			FROM counts
			ORDER BY person_name, matches DESC, registry_id
		),
		candidates AS (
			SELECT ps.slug, o.registry_id, o.matches
			FROM player_slug ps
			JOIN name_owner o ON o.person_name = ps.player_name
			WHERE ps.registry_id IS NULL
				AND NOT EXISTS (SELECT 1 FROM player_slug taken WHERE taken.registry_id = o.registry_id)
		),
		chosen AS (
			SELECT DISTINCT ON (registry_id) slug, registry_id
			FROM candidates
			ORDER BY registry_id, matches DESC, slug
		)
		UPDATE player_slug ps
		SET registry_id = c.registry_id
		FROM chosen c
		WHERE ps.slug = c.slug
	`)
	if err != nil {
		return 0, fmt.Errorf("assign legacy slug registry ids: %w", err)
	}
	return tag.RowsAffected(), nil
}

// SlugRefreshResult reports what RefreshPlayerSlugs changed.
type SlugRefreshResult struct {
	Players  int
	Inserted int
	Updated  int
	// Removed lists legacy slugs dropped because their person already owns
	// another slug (e.g. NR Sciver and NR Sciver-Brunt were two profiles of
	// one player) or no longer appears in the data under that name.
	Removed []string
	// Unregistered counts (match, name) batting/bowling appearances with no
	// wpl_person_registry row; those players can't get a profile.
	Unregistered int64
}

type slugRow struct {
	slug       string
	registryID *string
	playerName string
	leagues    []string
}

type slugPlayer struct {
	registryID string
	name       string
	leagues    []string
}

// RefreshPlayerSlugs creates and updates player_slug rows, one per Cricsheet
// registry ID that has batted or bowled. Existing slugs never change; only
// their name and league list are kept current. New players get a slug from
// their name, with -2, -3, ... appended on a clash.
func (db *DB) RefreshPlayerSlugs(ctx context.Context) (SlugRefreshResult, error) {
	var result SlugRefreshResult

	if _, err := db.AssignLegacySlugRegistryIDs(ctx); err != nil {
		return result, err
	}

	players, err := db.loadSlugPlayers(ctx)
	if err != nil {
		return result, err
	}
	result.Players = len(players)

	if err := db.pool.QueryRow(ctx, `
		WITH appearances AS (
			SELECT DISTINCT match_id, striker AS person_name FROM wpl_delivery
			UNION
			SELECT DISTINCT match_id, bowler FROM wpl_delivery
		)
		SELECT COUNT(*) FROM appearances a
		WHERE NOT EXISTS (
			SELECT 1 FROM wpl_person_registry r
			WHERE r.match_id = a.match_id AND r.person_name = a.person_name
		)
	`).Scan(&result.Unregistered); err != nil {
		return result, fmt.Errorf("count unregistered appearances: %w", err)
	}

	existing, err := db.loadSlugRows(ctx)
	if err != nil {
		return result, err
	}

	byRegistryID := make(map[string]slugRow)
	unlinkedByName := make(map[string]slugRow)
	taken := make(map[string]bool, len(existing))
	for _, row := range existing {
		taken[row.slug] = true
		if row.registryID != nil {
			byRegistryID[*row.registryID] = row
		} else {
			unlinkedByName[row.playerName] = row
		}
	}

	type slugUpdate struct {
		slug       string
		registryID string
		name       string
		leagues    []string
		rename     bool
	}
	var updates []slugUpdate
	var newPlayers []slugPlayer

	for _, p := range players {
		row, ok := byRegistryID[p.registryID]
		if !ok {
			row, ok = unlinkedByName[p.name]
			if ok {
				delete(unlinkedByName, p.name)
			}
		}
		if !ok {
			newPlayers = append(newPlayers, p)
			continue
		}

		linked := row.registryID != nil && *row.registryID == p.registryID
		if linked && row.playerName == p.name && slices.Equal(row.leagues, p.leagues) {
			continue
		}
		updates = append(updates, slugUpdate{
			slug:       row.slug,
			registryID: p.registryID,
			name:       p.name,
			leagues:    p.leagues,
			rename:     row.playerName != p.name,
		})
	}

	// Whatever wasn't adopted above is a second profile of someone who is
	// already linked, or a name no longer in the data.
	for _, row := range unlinkedByName {
		result.Removed = append(result.Removed, row.slug)
	}
	sort.Strings(result.Removed)

	sort.Slice(newPlayers, func(i, j int) bool {
		if newPlayers[i].name != newPlayers[j].name {
			return newPlayers[i].name < newPlayers[j].name
		}
		return newPlayers[i].registryID < newPlayers[j].registryID
	})

	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin slug refresh: %w", err)
	}
	defer tx.Rollback(ctx)

	if len(result.Removed) > 0 {
		if _, err := tx.Exec(ctx,
			`DELETE FROM player_slug WHERE slug = ANY($1) AND registry_id IS NULL`, result.Removed); err != nil {
			return result, fmt.Errorf("remove duplicate legacy slugs: %w", err)
		}
	}

	// player_name is unique, so free renamed rows' names first in case two
	// players swap names within one refresh.
	for _, u := range updates {
		if !u.rename {
			continue
		}
		if _, err := tx.Exec(ctx,
			`UPDATE player_slug SET player_name = '~' || slug WHERE slug = $1`, u.slug); err != nil {
			return result, fmt.Errorf("free name for slug %s: %w", u.slug, err)
		}
	}

	for _, u := range updates {
		if _, err := tx.Exec(ctx, `
			UPDATE player_slug
			SET registry_id = $2, player_name = $3, leagues = $4, updated_at = now()
			WHERE slug = $1
		`, u.slug, u.registryID, u.name, u.leagues); err != nil {
			return result, fmt.Errorf("update slug %s: %w", u.slug, err)
		}
	}
	result.Updated = len(updates)

	if len(newPlayers) > 0 {
		rows := make([][]interface{}, len(newPlayers))
		for i, p := range newPlayers {
			slug := nextFreeSlug(slugify(p.name, p.registryID), taken)
			taken[slug] = true
			rows[i] = []interface{}{slug, p.registryID, p.name, p.leagues}
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"player_slug"},
			[]string{"slug", "registry_id", "player_name", "leagues"}, pgx.CopyFromRows(rows)); err != nil {
			return result, fmt.Errorf("insert new slugs: %w", err)
		}
	}
	result.Inserted = len(newPlayers)

	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit slug refresh: %w", err)
	}

	return result, nil
}

// loadSlugPlayers returns every registry ID that has batted or bowled, with
// the name from its most recent match and the leagues it has played in.
func (db *DB) loadSlugPlayers(ctx context.Context) ([]slugPlayer, error) {
	rows, err := db.pool.Query(ctx, `
		WITH appearances AS (
			SELECT DISTINCT match_id, striker AS person_name FROM wpl_delivery
			UNION
			SELECT DISTINCT match_id, bowler FROM wpl_delivery
		)
		SELECT
			r.registry_id,
			(array_agg(r.person_name ORDER BY m.start_date DESC, m.match_id DESC))[1],
			array_agg(DISTINCT m.league ORDER BY m.league)
		FROM appearances a
		JOIN wpl_person_registry r ON r.match_id = a.match_id AND r.person_name = a.person_name
		JOIN wpl_match m ON m.match_id = a.match_id
		GROUP BY r.registry_id
	`)
	if err != nil {
		return nil, fmt.Errorf("load slug players: %w", err)
	}
	defer rows.Close()

	var players []slugPlayer
	for rows.Next() {
		var p slugPlayer
		if err := rows.Scan(&p.registryID, &p.name, &p.leagues); err != nil {
			return nil, fmt.Errorf("scan slug player: %w", err)
		}
		players = append(players, p)
	}
	return players, rows.Err()
}

func (db *DB) loadSlugRows(ctx context.Context) ([]slugRow, error) {
	rows, err := db.pool.Query(ctx, `SELECT slug, registry_id, player_name, leagues FROM player_slug`)
	if err != nil {
		return nil, fmt.Errorf("load player_slug: %w", err)
	}
	defer rows.Close()

	var result []slugRow
	for rows.Next() {
		var r slugRow
		if err := rows.Scan(&r.slug, &r.registryID, &r.playerName, &r.leagues); err != nil {
			return nil, fmt.Errorf("scan player_slug: %w", err)
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

var nonSlugChars = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// slugify matches the SQL used by migration 000008, so slugs created by the
// seeder look the same as the backfilled ones.
func slugify(name, fallback string) string {
	slug := strings.ToLower(strings.Trim(nonSlugChars.ReplaceAllString(name, "-"), "-"))
	if slug == "" {
		return strings.ToLower(fallback)
	}
	return slug
}

func nextFreeSlug(base string, taken map[string]bool) string {
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if !taken[candidate] {
			return candidate
		}
	}
}
