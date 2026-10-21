# Maintainer

Keep this a small Go CLI. Prefer the standard library and existing agent harnesses.
Use Charm for terminal UX; preserve noninteractive command equivalents.
Never equate prompt instructions with an enforced security boundary.
Do not install timers or run real maintenance as a side effect of development/tests.
Run `go test -race ./...`, `go vet ./...`, and `go build -o bin/maintain ./cmd/maintain` before publishing changes.
