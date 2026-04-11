package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// bowlingMapping holds the categorised attributes for a bowling style.
type bowlingMapping struct {
	hand    string // right, left, unknown
	bType   string // pace, spin, unknown
	subType string // fast, fast-medium, medium-fast, medium, offbreak, legbreak, left-arm-orthodox, left-arm-wrist-spin, slow, unknown
}

// bowlingStyleMap maps raw ESPN bowling style strings to structured categories.
var bowlingStyleMap = map[string]bowlingMapping{
	// Pace — Right arm
	"Right-arm fast":            {hand: "right", bType: "pace", subType: "fast"},
	"Right-arm fast (roundarm)": {hand: "right", bType: "pace", subType: "fast"},
	"Right-arm fast-medium":     {hand: "right", bType: "pace", subType: "fast-medium"},
	"Right-arm medium-fast":     {hand: "right", bType: "pace", subType: "medium-fast"},
	"Right-arm medium":          {hand: "right", bType: "pace", subType: "medium"},
	"Right-arm slow-medium":     {hand: "right", bType: "pace", subType: "medium"},

	// Pace — Left arm
	"Left-arm fast":            {hand: "left", bType: "pace", subType: "fast"},
	"Left-arm fast (roundarm)": {hand: "left", bType: "pace", subType: "fast"},
	"Left-arm fast-medium":     {hand: "left", bType: "pace", subType: "fast-medium"},
	"Left-arm medium-fast":     {hand: "left", bType: "pace", subType: "medium-fast"},
	"Left-arm medium":          {hand: "left", bType: "pace", subType: "medium"},
	"Left-arm slow-medium":     {hand: "left", bType: "pace", subType: "medium"},

	// Spin — Offbreak (right arm finger spin)
	"Right-arm offbreak":            {hand: "right", bType: "spin", subType: "offbreak"},
	"Right-arm offbreak (underarm)": {hand: "right", bType: "spin", subType: "offbreak"},

	// Spin — Legbreak (right arm wrist spin)
	"Legbreak":        {hand: "right", bType: "spin", subType: "legbreak"},
	"Legbreak googly": {hand: "right", bType: "spin", subType: "legbreak"},

	// Spin — Left-arm orthodox (left arm finger spin)
	"Slow left-arm orthodox": {hand: "left", bType: "spin", subType: "left-arm-orthodox"},

	// Spin — Left-arm wrist spin (chinaman)
	"Left-arm wrist-spin": {hand: "left", bType: "spin", subType: "left-arm-wrist-spin"},

	// Spin — Generic slow
	"Right-arm slow":              {hand: "right", bType: "spin", subType: "slow"},
	"Right-arm slow (underarm)":   {hand: "right", bType: "spin", subType: "slow"},
	"Right-arm slow (roundarm)":   {hand: "right", bType: "spin", subType: "slow"},
	"Left-arm slow":               {hand: "left", bType: "spin", subType: "slow"},

	// Unknown — generic bowler descriptions
	"Right-arm bowler":              {hand: "right", bType: "unknown", subType: "unknown"},
	"Left-arm bowler":               {hand: "left", bType: "unknown", subType: "unknown"},
	"(unknown arm) slow (underarm)": {hand: "unknown", bType: "spin", subType: "slow"},
	"(unknown arm) slow (roundarm)": {hand: "unknown", bType: "spin", subType: "slow"},
}

// playingRoleMap maps raw ESPN playing role to a collapsed category.
var playingRoleMap = map[string]string{
	"Batter":              "batter",
	"Top-order batter":    "batter",
	"Middle-order batter": "batter",
	"Opening batter":      "batter",
	"Bowler":              "bowler",
	"Allrounder":          "allrounder",
	"Batting allrounder":  "allrounder",
	"Bowling allrounder":  "allrounder",
	"Wicketkeeper":        "wicketkeeper",
	"Wicketkeeper batter": "wicketkeeper",
	"Unknown":             "unknown",
}

// playingRoleDetailMap maps raw ESPN playing role to a snake_case detail value.
var playingRoleDetailMap = map[string]string{
	"Batter":              "batter",
	"Top-order batter":    "top_order_batter",
	"Middle-order batter": "middle_order_batter",
	"Opening batter":      "opening_batter",
	"Bowler":              "bowler",
	"Allrounder":          "allrounder",
	"Batting allrounder":  "batting_allrounder",
	"Bowling allrounder":  "bowling_allrounder",
	"Wicketkeeper":        "wicketkeeper",
	"Wicketkeeper batter": "wicketkeeper_batter",
	"Unknown":             "unknown",
}

func main() {
	inputFile := flag.String("input", "player_attributes.csv", "Path to the input player_attributes.csv file")
	outputFile := flag.String("output", "player_styles.csv", "Path to the output CSV file")
	flag.Parse()

	fmt.Println("=== Player Style Categoriser ===")
	fmt.Printf("Input:  %s\n", *inputFile)
	fmt.Printf("Output: %s\n", *outputFile)

	// Read input
	records, header, err := readCSV(*inputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
		os.Exit(1)
	}

	// Build column index from header
	colIdx := make(map[string]int)
	for i, col := range header {
		colIdx[col] = i
	}

	requiredCols := []string{"identifier", "key_cricinfo", "name", "full_name", "batting_style", "bowling_style", "playing_role", "error"}
	for _, col := range requiredCols {
		if _, ok := colIdx[col]; !ok {
			fmt.Fprintf(os.Stderr, "Error: missing required column '%s' in input CSV\n", col)
			os.Exit(1)
		}
	}

	// Process and write output
	outFile, err := os.Create(*outputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output file: %v\n", err)
		os.Exit(1)
	}
	defer outFile.Close()

	writer := csv.NewWriter(outFile)
	defer writer.Flush()

	// Write header
	outHeader := []string{
		"identifier", "key_cricinfo", "name", "full_name",
		"batting_hand", "bowling_hand", "bowling_type", "bowling_sub_type",
		"playing_role", "playing_role_detail",
		"batting_style_raw", "bowling_style_raw",
	}
	if err := writer.Write(outHeader); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing header: %v\n", err)
		os.Exit(1)
	}

	var total, written, unmapped int

	for _, record := range records {
		total++

		identifier := record[colIdx["identifier"]]
		keyCricinfo := record[colIdx["key_cricinfo"]]
		name := record[colIdx["name"]]
		fullName := record[colIdx["full_name"]]
		battingStyleRaw := strings.TrimSpace(record[colIdx["batting_style"]])
		bowlingStyleRaw := strings.TrimSpace(record[colIdx["bowling_style"]])
		playingRoleRaw := strings.TrimSpace(record[colIdx["playing_role"]])

		// Categorise batting hand
		battingHand := categorizeBattingHand(battingStyleRaw)

		// Categorise bowling
		bowlingHand, bowlingType, bowlingSubType := categorizeBowling(bowlingStyleRaw)
		if bowlingStyleRaw != "" && bowlingSubType == "unmapped" {
			unmapped++
			fmt.Printf("  WARN: unmapped bowling style: %q (player: %s)\n", bowlingStyleRaw, name)
		}

		// Categorise playing role
		playingRole := categorizePlayingRole(playingRoleRaw)
		playingRoleDetail := categorizePlayingRoleDetail(playingRoleRaw)

		row := []string{
			identifier, keyCricinfo, name, fullName,
			battingHand, bowlingHand, bowlingType, bowlingSubType,
			playingRole, playingRoleDetail,
			battingStyleRaw, bowlingStyleRaw,
		}

		if err := writer.Write(row); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing row: %v\n", err)
			os.Exit(1)
		}
		written++
	}

	fmt.Printf("\nResults:\n")
	fmt.Printf("  Total rows read: %d\n", total)
	fmt.Printf("  Written:         %d\n", written)
	if unmapped > 0 {
		fmt.Printf("  ⚠ Unmapped styles: %d\n", unmapped)
	}
	fmt.Printf("\nOutput saved to: %s\n", *outputFile)
}

func readCSV(path string) ([][]string, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("opening file: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.LazyQuotes = true

	header, err := reader.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("reading header: %w", err)
	}

	var records [][]string
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue // skip malformed rows
		}
		records = append(records, record)
	}

	return records, header, nil
}

func categorizeBattingHand(raw string) string {
	switch raw {
	case "Right-hand bat":
		return "right"
	case "Left-hand bat":
		return "left"
	default:
		return ""
	}
}

func categorizeBowling(raw string) (hand, bType, subType string) {
	if raw == "" {
		return "", "", ""
	}

	if m, ok := bowlingStyleMap[raw]; ok {
		return m.hand, m.bType, m.subType
	}

	return "unknown", "unknown", "unmapped"
}

func categorizePlayingRole(raw string) string {
	if role, ok := playingRoleMap[raw]; ok {
		return role
	}
	return "unknown"
}

func categorizePlayingRoleDetail(raw string) string {
	if detail, ok := playingRoleDetailMap[raw]; ok {
		return detail
	}
	return "unknown"
}
