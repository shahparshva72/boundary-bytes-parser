.PHONY: build run clean test

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
