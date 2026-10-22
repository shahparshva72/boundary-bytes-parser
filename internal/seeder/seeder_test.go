package seeder

import (
	"testing"

	"github.com/shahparshva72/boundary-bytes-parser/internal/models"
	"github.com/shahparshva72/boundary-bytes-parser/internal/people"
)

func TestApplyUniqueNames(t *testing.T) {
	potm := "E Jones"
	dismissed := "E Jones"
	s := &Seeder{people: people.Register{
		"emma":  {Name: "E Jones", UniqueName: "Emma Jones"}, // clashes with another E Jones
		"kohli": {Name: "V Kohli", UniqueName: "V Kohli"},    // unique: must not change
	}}

	infos := []parsedInfo{{
		matchInfo: &models.MatchInfo{ID: 1, PlayerOfMatch: &potm},
		players: []models.Player{
			{MatchID: 1, PlayerName: "E Jones"},
			{MatchID: 1, PlayerName: "V Kohli"},
		},
		registry: []models.PersonRegistry{
			{MatchID: 1, PersonName: "E Jones", RegistryID: "emma"},
			{MatchID: 1, PersonName: "V Kohli", RegistryID: "kohli"},
		},
	}}
	matches := []parsedMatch{{
		match: &models.Match{ID: 1},
		deliveries: []models.Delivery{
			{Striker: "E Jones", NonStriker: "V Kohli", Bowler: "X", PlayerDismissed: &dismissed},
		},
	}}

	s.applyUniqueNames(infos, matches)

	d := matches[0].deliveries[0]
	if d.Striker != "Emma Jones" || *d.PlayerDismissed != "Emma Jones" {
		t.Errorf("delivery not renamed: striker=%q dismissed=%q", d.Striker, *d.PlayerDismissed)
	}
	if d.NonStriker != "V Kohli" || d.Bowler != "X" {
		t.Errorf("unaffected names changed: non_striker=%q bowler=%q", d.NonStriker, d.Bowler)
	}
	if infos[0].players[0].PlayerName != "Emma Jones" || infos[0].registry[0].PersonName != "Emma Jones" {
		t.Errorf("info rows not renamed: %+v %+v", infos[0].players[0], infos[0].registry[0])
	}
	if *infos[0].matchInfo.PlayerOfMatch != "Emma Jones" {
		t.Errorf("player_of_match not renamed: %q", *infos[0].matchInfo.PlayerOfMatch)
	}
}

func TestApplyUniqueNamesMapsShortNameAlias(t *testing.T) {
	// The info file registers "Carolyn Esterhuizen" but the ball-by-ball file
	// uses her short name, which is another person's unique_name.
	s := &Seeder{people: people.Register{
		"carolyn": {Name: "C Esterhuizen", UniqueName: "Carolyn Esterhuizen"},
	}}
	infos := []parsedInfo{{
		matchInfo: &models.MatchInfo{ID: 7},
		registry:  []models.PersonRegistry{{MatchID: 7, PersonName: "Carolyn Esterhuizen", RegistryID: "carolyn"}},
	}}
	matches := []parsedMatch{{
		match:      &models.Match{ID: 7},
		deliveries: []models.Delivery{{Striker: "C Esterhuizen", NonStriker: "Y", Bowler: "Z"}},
	}}

	s.applyUniqueNames(infos, matches)

	if got := matches[0].deliveries[0].Striker; got != "Carolyn Esterhuizen" {
		t.Errorf("short-name alias not mapped: striker=%q", got)
	}
}
