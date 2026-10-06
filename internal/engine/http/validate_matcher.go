package httpengine

import (
	"encoding/json"
	"net/http"

	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/validate"
)

// runValidation evaluates def's rules against reqCtx and, if any fail,
// writes the configured (or default) error response and returns true —
// callers should stop processing the request when it does.
func (e *Engine) runValidation(w http.ResponseWriter, def *mock.Definition, reqCtx mock.RequestContext) (handled bool) {
	if len(def.Validation) == 0 {
		return false
	}

	extract := func(field string) (any, bool, error) {
		return mock.ExtractField(field, reqCtx)
	}
	errs := validate.Evaluate(def.Validation, extract)
	if len(errs) == 0 {
		return false
	}

	if def.ValidationErrorResponse != nil {
		// No fault injection here deliberately: FaultConfig simulates a
		// flaky *working* backend, not corrupted error-reporting for a
		// request the caller already got wrong.
		e.writeTemplatedResponse(w, *def.ValidationErrorResponse, reqCtx, nil, def.ID)
		return true
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(map[string]any{"errors": errs})
	return true
}
