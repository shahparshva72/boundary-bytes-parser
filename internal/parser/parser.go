package parser

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shahparshva72/boundary-bytes-parser/internal/models"
)

type CSVParser struct {
	config models.LeagueConfig
}

func NewCSVParser(config models.LeagueConfig) *CSVParser {
	return &CSVParser{config: config}
}

func (p *CSVParser) GetMatchFiles() ([]string, []string, error) {
	entries, err := os.ReadDir(p.config.CSVDirectory)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read directory %s: %w", p.config.CSVDirectory, err)
	}

	var matchFiles, infoFiles []string

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".csv") {
			continue
		}

		fullPath := filepath.Join(p.config.CSVDirectory, entry.Name())

		if strings.Contains(entry.Name(), "_info") {
			infoFiles = append(infoFiles, fullPath)
		} else {
			matchFiles = append(matchFiles, fullPath)
		}
	}

	return matchFiles, infoFiles, nil
}

func (p *CSVParser) ParseMatchFile(filePath string) (*models.Match, []models.Delivery, error) {
	matchID, err := extractMatchID(filePath, false)
	if err != nil {
		return nil, nil, err
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1

	headers, err := reader.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read headers from %s: %w", filePath, err)
	}

	headerIndex := make(map[string]int)
	for i, h := range headers {
		headerIndex[strings.TrimSpace(h)] = i
	}

	var match *models.Match
	var deliveries []models.Delivery

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read record from %s: %w", filePath, err)
		}

		if match == nil {
			startDate, parseErr := parseDate(getField(record, headerIndex, "start_date"))
			if parseErr != nil {
				return nil, nil, fmt.Errorf("failed to parse start_date: %w", parseErr)
			}

			match = &models.Match{
				ID:        matchID,
				League:    p.config.League,
				Season:    getField(record, headerIndex, "season"),
				StartDate: startDate,
				Venue:     strings.Trim(getField(record, headerIndex, "venue"), "\""),
			}
		}

		delivery := models.Delivery{
			MatchID:              matchID,
			Innings:              parseInt(getField(record, headerIndex, "innings")),
			Ball:                 getField(record, headerIndex, "ball"),
			BattingTeam:          getField(record, headerIndex, "batting_team"),
			BowlingTeam:          getField(record, headerIndex, "bowling_team"),
			Striker:              getField(record, headerIndex, "striker"),
			NonStriker:           getField(record, headerIndex, "non_striker"),
			Bowler:               getField(record, headerIndex, "bowler"),
			RunsOffBat:           parseInt(getField(record, headerIndex, "runs_off_bat")),
			Extras:               parseInt(getField(record, headerIndex, "extras")),
			Wides:                parseInt(getField(record, headerIndex, "wides")),
			Noballs:              parseInt(getField(record, headerIndex, "noballs")),
			Byes:                 parseInt(getField(record, headerIndex, "byes")),
			Legbyes:              parseInt(getField(record, headerIndex, "legbyes")),
			Penalty:              parseInt(getField(record, headerIndex, "penalty")),
			WicketType:           getNullableString(getField(record, headerIndex, "wicket_type")),
			PlayerDismissed:      getNullableString(getField(record, headerIndex, "player_dismissed")),
			OtherWicketType:      getNullableString(getField(record, headerIndex, "other_wicket_type")),
			OtherPlayerDismissed: getNullableString(getField(record, headerIndex, "other_player_dismissed")),
		}

		deliveries = append(deliveries, delivery)
	}

	if match == nil {
		return nil, nil, fmt.Errorf("no data rows in file %s", filePath)
	}

	return match, deliveries, nil
}

func (p *CSVParser) ParseInfoFile(filePath string) (*models.MatchInfo, []models.Team, []models.Player, []models.Official, []models.PersonRegistry, error) {
	matchID, err := extractMatchID(filePath, true)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1

	matchInfo := &models.MatchInfo{
		ID:           matchID,
		League:       p.config.League,
		BallsPerOver: 6,
	}

	var teams []models.Team
	var players []models.Player
	var officials []models.Official
	var peopleRegistry []models.PersonRegistry

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, nil, nil, nil, fmt.Errorf("failed to read record from %s: %w", filePath, err)
		}

		if len(record) < 2 {
			continue
		}

		rowType := strings.TrimSpace(record[0])
		key := strings.TrimSpace(record[1])

		var value, extra string
		if len(record) > 2 {
			value = strings.TrimSpace(record[2])
		}
		if len(record) > 3 {
			extra = strings.TrimSpace(record[3])
		}

		if rowType == "version" {
			matchInfo.Version = key
			continue
		}

		if rowType != "info" {
			continue
		}

		switch key {
		case "balls_per_over":
			matchInfo.BallsPerOver = parseInt(value)
		case "team":
			teams = append(teams, models.Team{
				MatchID:  matchID,
				TeamName: value,
			})
		case "gender":
			matchInfo.Gender = value
		case "season":
			matchInfo.Season = value
		case "date":
			parsedDate, parseErr := parseDate(value)
			if parseErr == nil {
				matchInfo.Date = parsedDate
			}
		case "event":
			matchInfo.Event = value
		case "match_number":
			matchInfo.MatchNumber = parseInt(value)
		case "venue":
			matchInfo.Venue = strings.Trim(value, "\"")
		case "city":
			matchInfo.City = value
		case "toss_winner":
			matchInfo.TossWinner = value
		case "toss_decision":
			matchInfo.TossDecision = value
		case "player_of_match":
			matchInfo.PlayerOfMatch = &value
		case "winner":
			matchInfo.Winner = &value
		case "winner_runs":
			runs := parseInt(value)
			matchInfo.WinnerRuns = &runs
		case "winner_wickets":
			wickets := parseInt(value)
			matchInfo.WinnerWickets = &wickets
		case "umpire", "reserve_umpire", "tv_umpire", "match_referee":
			officials = append(officials, models.Official{
				MatchID:      matchID,
				OfficialType: key,
				OfficialName: value,
			})
		case "player":
			if extra != "" {
				players = append(players, models.Player{
					MatchID:    matchID,
					TeamName:   value,
					PlayerName: extra,
				})
			}
		case "registry":
			if value == "people" && extra != "" && len(record) > 4 {
				registryID := strings.TrimSpace(record[4])
				peopleRegistry = append(peopleRegistry, models.PersonRegistry{
					MatchID:    matchID,
					PersonName: extra,
					RegistryID: registryID,
				})
			}
		}
	}

	return matchInfo, teams, players, officials, peopleRegistry, nil
}

func extractMatchID(filePath string, isInfo bool) (int, error) {
	filename := filepath.Base(filePath)
	var pattern string
	if isInfo {
		pattern = `^(\d+)_info\.csv$`
	} else {
		pattern = `^(\d+)\.csv$`
	}

	re := regexp.MustCompile(pattern)
	matches := re.FindStringSubmatch(filename)
	if len(matches) < 2 {
		return 0, fmt.Errorf("could not extract match ID from filename: %s", filename)
	}

	return strconv.Atoi(matches[1])
}

func getField(record []string, index map[string]int, field string) string {
	if idx, ok := index[field]; ok && idx < len(record) {
		return strings.TrimSpace(record[idx])
	}
	return ""
}

func parseInt(s string) int {
	if s == "" {
		return 0
	}
	val, _ := strconv.Atoi(s)
	return val
}

func getNullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func parseDate(s string) (time.Time, error) {
	formats := []string{
		"2006-01-02",
		"2006/01/02",
	}

	for _, format := range formats {
		if t, err := time.Parse(format, s); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("unable to parse date: %s", s)
}
