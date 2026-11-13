package donations

import "time"

// backfillMaxAge bounds what a reconnect restores. An outage longer than this
// restores the last day; the alternative is flooding the queue with history.
const backfillMaxAge = 24 * time.Hour

// backfillSince is false for a credential that has never delivered an event:
// a first connection has nothing to restore.
func backfillSince(creds Credentials) (time.Time, bool) {
	if creds.LastEventAt == nil || creds.LastEventAt.IsZero() {
		return time.Time{}, false
	}
	since := *creds.LastEventAt
	if oldest := time.Now().Add(-backfillMaxAge); since.Before(oldest) {
		return oldest, true
	}
	return since, true
}
