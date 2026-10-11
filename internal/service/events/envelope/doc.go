// Package envelope renders the event envelope EventBridge delivers to targets
// and shapes a target's input from it: the InputPath selection and the
// InputTransformer template (docs/research/phase1/secrets-logs-events-alarms.md
// sections 4.3, 4.5 to 4.7; lambda-events.md section 5). Everything here is a
// pure function of its arguments.
//
// The envelope's members come in the recorded order: version ("0"), id,
// detail-type, source, account, time, region, resources and detail. The time
// is written in UTC to the second with a Z suffix, the form of every recorded
// and documented example.
//
// A path is the JSONPath subset the EventBridge documentation lists: "$",
// then member names in dot notation (letters, digits, "-", "_" and "/"),
// array indices in brackets and the wildcard, as ".*" or "[*]". A path without
// a wildcard selects one value or nothing; a path with one selects the list of
// every value it reaches.
//
// Template rendering follows the recorded transformer outputs:
//
//   - A template whose first non-blank character is a double quote is a string
//     template, delivered as written with its placeholders replaced: strings
//     raw, objects and arrays with every quote and blank removed
//     ({acc_id:0a787ecb-4015,sf_id:baz}, [ACTIVE,INACTIVE]).
//   - Any other template is a JSON template. A placeholder in value position
//     gets a string in quotes and any other value as JSON; a placeholder
//     inside a string literal gets a string or scalar raw, and an object or
//     array there ends the delivery, as AWS drops it. The output must be
//     JSON, or the delivery is dropped.
//   - Values are not escaped, as the documentation states, so a string value
//     with a quote breaks a JSON template's output, which is then dropped.
//   - A variable whose path selects nothing renders as the empty string ("" in
//     value position).
//
// Decisions the research leaves open:
//
//   - The aws.events.event variable lists id, account, detailType, time,
//     source, region, resources and version, the recorded members and order;
//     its time is the envelope's.
//   - aws.events.event.ingestion-time has the envelope's time format.
//   - A caller-supplied event Time is rendered in the same format, so its
//     fraction of a second is dropped (UNVERIFIED, Q7 in the research).
package envelope
