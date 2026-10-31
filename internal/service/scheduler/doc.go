// Package scheduler implements Amazon EventBridge Scheduler (scheduler.md):
// schedule groups, schedules with at(), rate() and cron() expressions in
// IANA time zones, flexible time windows, and templated Lambda, SQS and
// EventBridge event bus targets invoked on the emulator clock as the
// schedule's execution role, with the retry policy and dead-letter queue of
// scheduler.md 7.
//
// Built so far: CreateSchedule (rate() with an SQS queue target in the
// default group), GetSchedule and DeleteSchedule; every other operation
// stays unbound and answers 501 until its ticket builds it.
package scheduler
