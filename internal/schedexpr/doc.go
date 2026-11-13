// Package schedexpr parses EventBridge rate and cron schedule expressions and
// computes the next fire time (docs/research/secrets-logs-events-alarms.md
// section 4.2).
//
// Parse returns a Rate or a Cron; every invalid expression returns an Error
// whose Message is AWS's single recorded text, "Parameter ScheduleExpression
// is not valid.". Next works on time.Time in UTC so the events service can
// drive it from the emulator clock: for a Rate it adds one interval to the
// anchor or previous fire, for a Cron it returns the first whole UTC minute
// strictly after the given instant that satisfies every field.
//
// ParseScheduler and NextIn serve EventBridge Scheduler (scheduler.md 5,
// 14.3): at() besides rate and cron, rate units per RateUnits, Scheduler's
// own refusal text echoing the expression, cron fields separated by runs
// of spaces and tabs, and cron evaluated on civil minutes in a time zone.
// Parse, Next and the rules' text are unchanged.
//
// Choices made here where the documentation is silent (Q4 in the research):
//   - Day-of-month and day-of-week: exactly one must be ?, so both ? is
//     rejected as well as both specified.
//   - Ranges in the cyclic fields may wrap (20-2), as the documentation's own
//     example implies; a year range may not.
//   - Month and day names are accepted in any case. Rate units stay
//     case-sensitive, as recorded. The special characters L, W and # are
//     uppercase only, and nL takes a day number, as in AWS's 6L example:
//     a name before L (SATL) is rejected, while a name before # (SAT#3) is
//     accepted, as AWS documents.
//   - L, LW, L-n, nW, nL and w#n stand alone in their field; they are
//     rejected inside lists. L alone in day-of-week is Saturday, the last
//     day of the week. nW never fires in a month shorter than n days and, as
//     in Quartz, never moves into a neighboring month.
//   - Day-of-month L-n, which AWS accepts (events/R14/01-07-put.json,
//     scheduler/SCH/03-e16.json), fires on the month's last day minus n, as
//     in Quartz; n runs from 0 to 30, Quartz's bound, and L-n never fires in
//     a month of n days or fewer. Both are inferred: no fire was recorded.
//   - A step of 0, which AWS accepts in the minutes field
//     (events/R14/01-26-put.json, scheduler/SCH/03-e19.json), is accepted in
//     every field that takes /: v/0 is the single value v, a-b/0 is a and
//     */0 is the field's first value. Inferred: no fire was recorded.
//   - The year field names any year from 1000 to 9999; only 1970-2199, the
//     documented range (scheduler.md 5.3), fire. AWS accepts 1969 and 2200
//     (events/R14/01-18-put.json, 01-19-put.json), and Scheduler refuses them
//     as never firing (scheduler/SCH/03-e17.json, 03-e18.json). The accepted
//     range beyond those two years is inferred.
//   - Field separation differs by dialect. Rules take exactly one ASCII
//     space between cron fields and refuse a double space or a tab
//     (events/R14/01-29-put.json, 01-30-put.json); Scheduler takes runs of
//     ASCII spaces and tabs (scheduler/SCH/03-e20.json, 03-e21.json). A rule
//     stored by an earlier build, which took the runs, loads through
//     ParseStored, which takes them too. Other whitespace, such as a
//     no-break space, is rejected by both. Rate needs exactly one space.
//     Neither may be padded inside the parentheses.
package schedexpr
