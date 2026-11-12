.PHONY: up down migrate proto test loadtest

up:
	docker compose up --build -d postgres redis core edge

down:
	docker compose down

migrate:
	docker compose run --rm migrate

proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative proto/commerce.proto

test:
	go test ./...

loadtest:
	k6 run scripts/loadtest/contention.js
