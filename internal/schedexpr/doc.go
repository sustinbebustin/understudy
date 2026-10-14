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
//   - L, LW, nW, nL and w#n stand alone in their field; they are rejected
//     inside lists. L alone in day-of-week is Saturday, the last day of the
//     week. nW never fires in a month shorter than n days and, as in Quartz,
//     never moves into a neighboring month.
//   - Cron fields may be separated by runs of ASCII spaces and tabs; other
//     whitespace, such as a no-break space, is rejected. Rate needs exactly
//     one space. Neither may be padded inside the parentheses.
package schedexpr
