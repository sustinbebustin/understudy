// Package logsfilter compiles and matches CloudWatch Logs filter patterns:
// unstructured terms, JSON patterns, space-delimited patterns and %regex%
// terms, the one grammar FilterLogEvents, metric filters and subscription
// filters share (docs/research/phase1/logs-filter-patterns.md).
//
// Compile a pattern once with Compile or Options.Compile where it enters the
// API, then call Match per event. A pattern AWS would reject returns an
// Error whose Message the logs service sends as an InvalidParameterException.
//
// Two questions no AWS recording settles are Options fields, each exposed by
// the logs service as a fidelity switch with the metadata Options.Switches
// returns. Each is decided in one function (termMatches,
// fieldCountAllows), so a recording changes one place and the table rows
// that name the switch.
//
// Smaller choices made here without a recording, each pinned by a case in
// residual_test.go:
//   - Error text: only the over-long pattern text is recorded. An unquoted
//     term with an illegal character uses the console wording "Invalid
//     character(s) in term <term>". Every other syntax error reads "Invalid
//     filter pattern: <problem> at offset <n>."
//   - Whitespace is ASCII space, tab, CR and LF everywhere in the grammar. A
//     blank term or pattern (rule N2) is made only of those, so a phrase of
//     no-break or other Unicode spaces is searched for as text.
//   - Unquoted terms accept alphanumerics and _ = : # @ / ; , . only, as in
//     the research grammar; anything else, including * and -, needs quotes.
//   - JSON typing: a number value compares only with JSON numbers; a string,
//     glob or regex value compares with JSON strings and with the source
//     text of JSON numbers. Bare true is the string "true"; booleans need
//     IS TRUE or IS FALSE. A bare * and a quoted lone "*" both match any
//     scalar, null included, because AWS documents both as presence. !=
//     holds for a present scalar that = does not match, so it never matches
//     a missing field.
//   - A JSON pattern needs the whole message to be one JSON value; text
//     before or after it means no match. A duplicate key keeps its last value.
//   - Space-delimited fields: a [ or " without its closer starts an ordinary
//     field. A field that reads as a number compares numerically. Under the
//     default field-count rule the last pattern field takes the raw text from
//     its own start to the end of the event.
//   - Numbers: a field or JSON number beyond float64 range compares as
//     infinity; such a number in a pattern is rejected rather than read as a
//     bare string.
//   - %regex% runs on Go's RE2 after the rule N4 character check; $ means end
//     of text and ^ start of text, with no multi-line mode and no flags. A
//     body RE2 rejects reads "invalid regular expression", without RE2's own
//     wording.
package logsfilter
