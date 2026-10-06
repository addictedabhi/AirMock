package mock

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AdvanceScenarioStep returns the step to serve for this hit (the session's
// last-persisted step, or 0 on first contact), then persists the next step
// for the following hit — clamped at the last step, or wrapped to 0 when
// loop is true. Splitting "step to serve now" from "step to serve next"
// this way is what makes the 3-hits-in-a-row-see-3-different-responses
// behavior work: the step advances only after being read, not before.
func (s *Store) AdvanceScenarioStep(mockID, sessionKey string, totalSteps int, loop bool) (int, error) {
	if totalSteps <= 0 {
		return 0, fmt.Errorf("scenario has no steps")
	}

	var current int
	row := s.db.QueryRow(`SELECT step FROM mock_state WHERE mock_id=? AND session_key=?`, mockID, sessionKey)
	err := row.Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("read scenario state: %w", err)
	}
	// sql.ErrNoRows leaves current at its zero value (0) — first-ever hit.

	next := current + 1
	if next >= totalSteps {
		if loop {
			next = 0
		} else {
			next = totalSteps - 1
		}
	}

	_, err = s.db.Exec(
		`INSERT INTO mock_state (mock_id, session_key, step, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(mock_id, session_key) DO UPDATE SET step=excluded.step, updated_at=excluded.updated_at`,
		mockID, sessionKey, next, formatTime(time.Now().UTC()),
	)
	if err != nil {
		return 0, fmt.Errorf("persist scenario state: %w", err)
	}
	return current, nil
}
