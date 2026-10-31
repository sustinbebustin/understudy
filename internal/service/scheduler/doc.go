// Package scheduler implements Amazon EventBridge Scheduler (scheduler.md):
// schedule groups, schedules with at(), rate() and cron() expressions in
// IANA time zones, flexible time windows, and templated Lambda, SQS, SNS
// and EventBridge event bus targets invoked on the emulator clock as the
// schedule's execution role, with the retry policy and dead-letter queue of
// scheduler.md 7, and tags on schedule groups. Every operation is bound;
// what each serves is its coverage note.
package scheduler

import (
	// The release image has no zone files (ARCHITECTURE.md 24), so the
	// IANA database time.LoadLocation reads is embedded (scheduler.md 1
	// item 6, 14.3.2).
	_ "time/tzdata"
)
