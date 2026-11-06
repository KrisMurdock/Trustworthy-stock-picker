package main

import (
	"context"
	"strings"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/aris"
)

func executeDiscovery(ctx context.Context, app *aris.App, command, request, planPath, id string, emit func(any) int, fail func(error) int) int {
	var state aris.SearchState
	var err error
	switch command {
	case "discover":
		var plan aris.SearchPlan
		var bundle aris.Bundle
		if err = readJSON(planPath, &plan); err != nil {
			return fail(err)
		}
		if err = readJSON(request, &bundle); err != nil {
			return fail(err)
		}
		state, err = app.Search(ctx, bundle, plan)
	case "discover-resume":
		state, err = app.ResumeSearch(ctx, id)
	case "discover-status":
		state, err = app.SearchStatus(id)
		if err != nil {
			return fail(err)
		}
		return emit(state)
	case "discover-cancel":
		if err = app.CancelSearch(id); err != nil {
			return fail(err)
		}
		return emit(map[string]string{"id": id, "status": "cancellation_requested"})
	case "discover-verify":
		if err = app.VerifySearch(ctx, id); err != nil {
			return fail(err)
		}
		return emit(map[string]any{"id": id, "verified": true, "scope": "complete adaptive development sequence and isolated validation"})
	}
	if err != nil {
		return fail(err)
	}
	if code := emit(state); code != 0 {
		return code
	}
	// Successful completion is an execution outcome, never production promotion.
	if strings.HasPrefix(state.Status, "completed_") {
		return 0
	}
	return 3
}
