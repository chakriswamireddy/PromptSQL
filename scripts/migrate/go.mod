module github.com/governance-platform/scripts/migrate

go 1.22

require (
	github.com/governance-platform/pkg/migrate v0.0.0
	github.com/rs/zerolog v1.33.0
)

replace github.com/governance-platform/pkg/migrate => ../../pkg/migrate
