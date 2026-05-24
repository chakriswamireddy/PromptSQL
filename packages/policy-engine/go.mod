module github.com/governance-platform/policy-engine

go 1.22

require (
	github.com/google/re2 v0.0.0-20240203163516-a00fa02e6598
	github.com/governance-platform/pkg/auth v0.0.0
)

replace (
	github.com/governance-platform/pkg/auth => ../../pkg/auth
)
