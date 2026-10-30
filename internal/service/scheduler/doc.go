// Package scheduler implements Amazon EventBridge Scheduler (scheduler.md):
// schedule groups, schedules with at(), rate() and cron() expressions in
// IANA time zones, flexible time windows, and templated Lambda, SQS and
// EventBridge event bus targets invoked on the emulator clock as the
// schedule's execution role, with the retry policy and dead-letter queue of
// scheduler.md 7.
//
// The service is not wired into the server yet: its operations answer 501
// NotImplemented "not implemented yet" until each ticket builds them.
package scheduler
