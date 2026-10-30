// Package people loads Cricsheet's people register (people.csv).
//
// Cricsheet's ball-by-ball `name` is not unique across people ("E Jones" is
// three different players). The register's `unique_name` column is, and it
// equals `name` for almost everyone, so the seeder stores unique_name to keep
// different people apart without changing anyone else's name.
package people

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
)

// Person is one row of the register.
type Person struct {
	// Name is the short name Cricsheet uses in ball-by-ball data.
	Name string
	// UniqueName is globally unique; it equals Name unless Name is shared.
	UniqueName string
}

// Register maps a Cricsheet registry identifier to the person.
type Register map[string]Person

// UniqueNames returns registry identifier -> unique_name.
func (r Register) UniqueNames() map[string]string {
	names := make(map[string]string, len(r))
	for id, p := range r {
		names[id] = p.UniqueName
	}
	return names
}

func Load(path string) (Register, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open people register: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read people register header: %w", err)
	}

	idCol, nameCol, uniqueCol := -1, -1, -1
	for i, h := range header {
		switch h {
		case "identifier":
			idCol = i
		case "name":
			nameCol = i
		case "unique_name":
			uniqueCol = i
		}
	}
	if idCol < 0 || nameCol < 0 || uniqueCol < 0 {
		return nil, fmt.Errorf("people register %s is missing identifier/name/unique_name columns", path)
	}

	register := make(Register)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read people register: %w", err)
		}
		if idCol >= len(record) || nameCol >= len(record) || uniqueCol >= len(record) {
			continue
		}
		if record[idCol] != "" && record[uniqueCol] != "" {
			register[record[idCol]] = Person{Name: record[nameCol], UniqueName: record[uniqueCol]}
		}
	}

	return register, nil
}
