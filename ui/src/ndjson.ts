// Line splitting for the journal's NDJSON stream, read with fetch and a
// ReadableStream (no EventSource, so the session header can be sent).

// lines yields each non-blank line of body without its "\n" or a trailing
// "\r", the last even when unterminated. UTF-8 is decoded across chunk
// boundaries, so a character split between two reads arrives whole.
export async function* lines(body: ReadableStream<Uint8Array>): AsyncGenerator<string> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let pending = "";
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }
      pending += decoder.decode(value, { stream: true });
      for (let end = pending.indexOf("\n"); end >= 0; end = pending.indexOf("\n")) {
        const line = withoutCR(pending.slice(0, end));
        pending = pending.slice(end + 1);
        if (line.trim() !== "") {
          yield line;
        }
      }
    }
    const last = withoutCR(pending + decoder.decode());
    if (last.trim() !== "") {
      yield last;
    }
  } finally {
    reader.releaseLock();
  }
}

const withoutCR = (line: string): string => (line.endsWith("\r") ? line.slice(0, -1) : line);
