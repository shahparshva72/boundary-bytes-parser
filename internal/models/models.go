package models

import "time"

type Match struct {
	ID        int
	League    string
	Season    string
	StartDate time.Time
	Venue     string
}

type Delivery struct {
	ID                   int
	MatchID              int
	Innings              int
	Ball                 string
	BattingTeam          string
	BowlingTeam          string
	Striker              string
	NonStriker           string
	Bowler               string
	RunsOffBat           int
	Extras               int
	Wides                int
	Noballs              int
	Byes                 int
	Legbyes              int
	Penalty              int
	WicketType           *string
	PlayerDismissed      *string
	OtherWicketType      *string
	OtherPlayerDismissed *string
}

type MatchInfo struct {
	ID            int
	League        string
	Version       string
	BallsPerOver  int
	Gender        string
	Season        string
	Date          time.Time
	Event         string
	MatchNumber   int
	Venue         string
	City          string
	TossWinner    string
	TossDecision  string
	PlayerOfMatch *string
	Winner        *string
	WinnerRuns    *int
	WinnerWickets *int
}

type Team struct {
	ID       int
	MatchID  int
	TeamName string
}

type Player struct {
	ID         int
	MatchID    int
	TeamName   string
	PlayerName string
}

type Official struct {
	ID           int
	MatchID      int
	OfficialType string
	OfficialName string
}

type PersonRegistry struct {
	ID         int
	MatchID    int
	PersonName string
	RegistryID string
}

type LeagueConfig struct {
	League       string
	CSVDirectory string
}
