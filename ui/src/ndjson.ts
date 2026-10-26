// Line splitting for the journal's NDJSON stream, read with fetch and a
// ReadableStream (no EventSource, so the session header can be sent).

// lines yields each non-blank line of body without its line end. Not
// implemented yet: it yields nothing.
export async function* lines(_body: ReadableStream<Uint8Array>): AsyncGenerator<string> {}
