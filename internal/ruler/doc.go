// Package ruler is the Event Ruler core shared by EventBridge event
// patterns, SNS filter policies and Lambda event source mapping filters
// (docs/research/matching-engines.md). Compile turns a pattern into a
// typed Pattern for a Profile; Match evaluates it against a JSON event and
// MatchAttributes against SNS message attributes. Both are pure functions.
//
// The grammar, error texts and matching semantics follow Ruler v2.2.0's
// strict compiler and matcher (commit ced786201c1f), verified against the 85
// seed vectors of matching-engines.md section 9 and against executed Ruler
// (vectors_test.go). Each Profile adds its service's operator allow-list,
// limits and error wording, which Ruler does not have. The four behaviors
// where Ruler and a service's documentation disagree are Options fields that
// default to Ruler. A profile fixes the fields its service's recordings
// settle; Switches describes the open ones, and the profiles' unverified
// allow-list and limit choices, for the fidelity ledger.
//
// Executed Ruler is the oracle, kept as a differential harness behind the
// rulerdiff build tag, outside the default test run. rulerdiff_test.go gives
// every case of the corpus in rulerdiff_corpus_test.go (the seed vectors,
// the IP range vectors, the Ruler defects below, and pattern groups for
// every operator, nested keys, "$or", arrays of arrays and malformed input,
// each crossed with its events) to Compile and Match under the EventBridge
// profile and to the Java Event Ruler that testdata/rulerdiff/pom.xml pins:
// the 2.2.0 release on Maven Central, two commits before ced786201c1f, which
// change only how Ruler deletes rules. It fails on every case where the
// match result, or the acceptance or rejection text of the pattern, differs
// and the difference is not a known one, and on a known one that no longer
// occurs. Run it with "just ruler-diff", which needs Docker or a JDK 21 with
// Maven (testdata/rulerdiff/run.sh picks one; RULERDIFF_RUNTIME forces it).
// A new difference is fixed here with a regression test, or, where Ruler and
// AWS are known to differ, AWS's answer is kept and the case is listed as
// known with its evidence. The corpus is the place to add a case; a test in
// the default run checks that it still uses every operator.
//
// Two Ruler behaviors are easy to miss. An {"exists": false} is checked when
// Ruler's traversal of the sorted keys reaches it, against the array elements
// the keys sorted before it pinned (ACFinder.tryMustNotExistMatch calls
// NameState.getNameTransitions with the membership so far), so a field that
// only a later key rules out still breaks it. Keys sort by UTF-16 code unit.
// And equals-ignore-case, with its prefix, suffix and anything-but forms,
// accepts per UTF-16 code unit of the operand exactly Java's full lower- and
// upper-case forms (input.EqualsIgnoreCaseParser), so "ß" accepts "SS" and
// "ǅ" does not accept itself.
//
// Decisions where neither the architecture nor an AWS recording settles the
// behavior:
//
//   - The package imports nothing in the repository (ARCHITECTURE.md section
//     2), so Switches returns plain metadata and the owning service builds
//     the fidelity.Switch values. Options travel inside each Profile so
//     Compile keeps its two-argument form.
//   - Malformed "$or" shapes are compile errors (the strict compiler). With
//     MalformedOrOrdinaryKey, which EventBridge has, a rejected pattern is
//     read again with "$or" as a plain key, and that reading's rejection is
//     reported when it has one; a malformed "$or" is never accepted
//     (events/R14 H10). The second reading is the strict grammar with "$or"
//     as a key, not all of Ruler's legacy RuleCompiler, whose other
//     differences (anything-but wildcard) are not recorded for any service.
//   - Every profile accepts Ruler's "exactly", which no service documents
//     but all three accept (matching-engines.md H7, H13, H14).
//   - SNS accepts anything-but suffix, which its current documentation lists
//     although a 2024 recording rejected it (H7), and the list forms of
//     anything-but prefix, suffix and wildcard, which the documentation
//     shows only with one string (H7). A non-string operand of an
//     anything-but object form gets Ruler's shape text, which SNS answers
//     too (sns/R09 H7b-08).
//   - Lambda accepts the operators its filtering table lists and those it
//     was recorded accepting: cidr, wildcard, anything-but with prefix or
//     suffix, and prefix with equals-ignore-case (H13). It refuses suffix
//     with equals-ignore-case and anything-but with equals-ignore-case or
//     wildcard, which neither its table nor a recording covers. Every Lambda
//     rejection carries the generic recorded message. On Lambda a string rule
//     also matches a number with the same text (H8), and a wildcard rule
//     matches nothing, from one recording without a positive control (H13).
//   - SNS counts distinct leaf keys across the whole policy, "$or" branches
//     included (sns/R09 H3, T12); a key repeated in two branches counts
//     once, which no recording covers. It sums the
//     combinations of the branches with each key weighted by its nesting
//     level, and scores wildcard points only for fields with a wildcard, at 3
//     points per "*" when a pattern has more than one (H11). The size and
//     wildcard texts are recorded (sns/R09 T16, H11, T13). The limits apply in the
//     order nesting, keys, combinations, wildcards, except that a policy
//     certain to exceed 150 combinations while it is read is reported as too
//     complex before its keys are counted in full.
//   - EventBridge caps the product of all "$or" array sizes at 1000, nested
//     arrays included (H12, with its recorded text), and rejects a field
//     whose wildcard complexity reaches 11 on Ruler's scale, the most
//     permissive cap consistent with the recorded accept at 7 and reject at
//     11. Pattern length limits that the Smithy models declare are left to
//     the generated validators.
//   - A pattern certain to exceed a limit stops expanding "$or" but is read
//     to the end, so a later grammar error is still the one reported. Every
//     profile stops expanding after maxExpansion steps, an engine bound with
//     no AWS counterpart that only Lambda, which has no combination limit,
//     reaches within a service's pattern length.
//   - Matching has the matching bound: one Match or MatchAttributes call
//     stops after maxSearchSteps units of work with a *SearchBudgetError, an
//     engine bound with no AWS counterpart. The units count the indexing of
//     the fields at the pattern's paths and of the arrays they share, done
//     once per call, the split of each branch into independent components,
//     and the search of each component, whose steps cost one unit per
//     element chosen so far. Exact array-consistent matching is NP-hard when
//     one path has fields in several arrays, and a 2.5 KB event of that shape
//     would otherwise take 20 s; with the bound, a call gives up within about
//     0.2 s and 50 MB. Callers record the error and do not deliver.
//   - A Number attribute in the MessageAttributes scope is a JSON number
//     (matching-engines.md 5.4, inference from a recorded numeric match), so
//     {"p":["100"]} and {"p":[{"prefix":"1"}]} never match Number 100 and
//     {"p":[{"anything-but":"100"}]} does. sns.md 6.3 reads the documentation
//     as comparing the StringValue text; the fixed switch
//     sns.filter-policy.number-attribute-type records the choice.
//   - Malformed JSON gets the messages of Jackson 2.22.2's
//     ReaderBasedJsonParser, the parser Ruler uses (jackson.go): its order of
//     reading (a scalar value with its field name, a string value only when
//     its text is needed), its start-marker locations and its limits of 1000
//     nested containers, 1000 number digits and 50000 name characters. An
//     event's messages name its source as REDACTED, as Jackson's defaults,
//     with which Ruler reads events, do. A one-off run of 30000 mutated
//     patterns against executed Ruler v2.2.0 (commit ced786201c1f), beyond
//     the harness's fixed corpus, found no differences in the first line of
//     a message besides these known ones: for a name over the limit Jackson reports the
//     length it had buffered, which depends on its buffers, where this
//     package reports the full length; a pattern over 32768 characters is
//     read through a buffer, and a number that crosses a buffer boundary is
//     counted on Jackson's secondary path, which can differ by one digit;
//     Java's identifier character classes follow its own Unicode version;
//     and an invalid UTF-8 byte, which Ruler never sees in its decoded
//     String, gets Jackson's byte parser message.
//   - A pattern's rejection ends with Jackson's location line, "\n at
//     [Source: (String)\"...\"; line: 1, column: N]", as Ruler's
//     JsonParseException message does and EventBridge and SNS answer
//     (events/R14 H10; sns/R09 H5, H7b-08, H10): where the parser stood,
//     a string value's body unread until its text is needed and a field's
//     string or container value begun with its name. The service limits
//     and the cidr operand errors carry none, as AWS answers them (H9, H11,
//     H12), and an anything-but list with null or a boolean names its
//     location twice, as Ruler rethrows it. The harness compares the whole
//     message; the locations of the 30000-pattern run above were not
//     checked. SNS hands Ruler the policy as it writes its parsed tree back
//     (reserialize), so its locations quote that text. SNS's own syntax
//     errors ("failed to parse JSON. ") name SNS's parser, which no
//     recording shows whole, and are left without a location; so are
//     event errors.
//   - With EmptyInputNeverMatches, which SNS has in MessageAttributes scope
//     (sns/R09 T06, H1-attr), "an empty set of attributes" counts
//     attributes, Binary ones included, or the members of an event, not the
//     values they flatten to: {"x":[]} and a String.Array "[]" are not empty.
//   - A cidr, and a string rule that is an IP literal, match as Ruler's
//     range byte machine does (ipRange), length-blind: "0.0.0.0/0" takes
//     IPv6 addresses, which AWS's recordings show, and numbers by their
//     text, which follows from the same machine but is not recorded. A
//     numeric rule anywhere at the same path stops a number's text being
//     walked, where Ruler stops it only for rules reached through the same
//     earlier keys of a "$or" branch.
//   - Content after an event's closing brace makes it malformed, as it does
//     a pattern; Ruler's rulesForJSONEvent reads the first value and ignores
//     the rest. AWS's answer is not recorded.
//
// Where executed Ruler v2.2.0 contradicts its own documented semantics, this
// package keeps the documented outcome (TestRulerDefectsNotCopied):
// Machine.rulesForJSONEvent and rulesForEvent both lose the values of a later
// "$or" member's array when an earlier member has the same key and shares a
// value, so {"$or":[{"x":["q"]},{"x":["q","y"]}]} misses {"x":"y"} and
// matches it with the members swapped (the fixed switch
// events.pattern.or-shared-values records the choice, H14); for the variant
// with the shared value in the middle, and for the one where a member holds
// {"exists": false}, Ruler's answer changes between Machine instances in one
// JVM; an anything-but wildcard next to an anything-but equals-ignore-case
// on one field misses values neither excludes; a numeric bound of
// 1.7976931348623157e308 throws while the rule is added; and a pattern key
// under an empty key, as in {"":{"a":["x"]}}, compiles to the path "a"
// while the event {"":{"a":"x"}} flattens to ".a", so Ruler matches
// {"a":"x"} instead. Here both sides join every step with ".", as Ruler's
// Event does.
package ruler
