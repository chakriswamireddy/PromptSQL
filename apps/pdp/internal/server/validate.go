package server

import (
	"context"

	pdpv1 "github.com/governance-platform/pkg/pdpv1"
	"github.com/governance-platform/policy-engine/dsl"
)

// Validate runs DSL validation on a draft conditions payload without persisting anything.
// Used by the admin console to give authors immediate feedback while editing.
func (s *Server) Validate(_ context.Context, req *pdpv1.ValidateRequest) (*pdpv1.ValidateResponse, error) {
	var errs []string

	n, err := dsl.Parse(req.Conditions)
	if err != nil {
		return &pdpv1.ValidateResponse{Valid: false, Errors: []string{err.Error()}}, nil
	}
	if err := dsl.Validate(n); err != nil {
		errs = append(errs, err.Error())
	}
	// Attempt compile to catch any closure-build errors.
	if len(errs) == 0 {
		if _, err := dsl.Compile(n); err != nil {
			errs = append(errs, "compile: "+err.Error())
		}
	}
	valid := len(errs) == 0
	return &pdpv1.ValidateResponse{Valid: valid, Errors: errs}, nil
}
