.PHONY: build run clean test build-scraper run-scraper scrape-test build-categoriser run-categoriser build-player-styles run-player-styles

build:
	go build -o bin/seeder ./cmd/seeder

build-prod:
	go build -ldflags="-s -w" -o bin/seeder ./cmd/seeder

run:
	go run ./cmd/seeder

run-wpl:
	go run ./cmd/seeder -league WPL

run-ipl:
	go run ./cmd/seeder -league IPL

run-bbl:
	go run ./cmd/seeder -league BBL

run-wbbl:
	go run ./cmd/seeder -league WBBL

run-sa20:
	go run ./cmd/seeder -league SA20

test:
	go test ./...

clean:
	rm -rf bin/

tidy:
	go mod tidy

build-scraper:
	go build -o bin/scraper ./cmd/scraper

run-scraper:
	go run ./cmd/scraper -input people.csv -output player_attributes.csv -workers 10

scrape-test:
	go run ./cmd/scraper -input people.csv -output player_attributes.csv -workers 5 -limit 20

build-categoriser:
	go build -o bin/categoriser ./cmd/categoriser

run-categoriser:
	go run ./cmd/categoriser -input player_attributes.csv -output player_styles.csv

build-player-styles:
	go build -o bin/seeder-player-styles ./cmd/seeder-player-styles

run-player-styles:
	go run ./cmd/seeder-player-styles -input player_styles.csv

