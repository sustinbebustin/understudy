// The UI session (ADR 0020): the secret arrives in the URL fragment, is
// kept in memory only and goes back on every admin call in a header.

export const SESSION_HEADER = "Understudy-UI-Session";

export type Credentials = { readonly secret: string; readonly token: string | null };

// secretFromHash returns the s parameter of a location hash ("#s=<secret>").
// Not implemented yet: it returns null.
export function secretFromHash(_hash: string): string | null {
  return null;
}

// withoutFragment returns href up to, not including, its "#". Not
// implemented yet: it returns "".
export function withoutFragment(_href: string): string {
  return "";
}

// headersFor returns the headers every admin call sends. Not implemented
// yet: it returns none.
export function headersFor(_c: Credentials): Record<string, string> {
  return {};
}
