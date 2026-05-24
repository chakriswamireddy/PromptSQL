module github.com/governance-platform/pkg/migrate

go 1.22

require (
	github.com/golang-migrate/migrate/v4 v4.17.1
	github.com/governance-platform/pkg/testdb v0.0.0
	github.com/lib/pq v1.10.9
	github.com/rs/zerolog v1.33.0
)

require (
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/sys v0.21.0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
)

replace github.com/governance-platform/pkg/testdb => ../testdb
