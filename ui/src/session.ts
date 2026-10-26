// The UI session (ADR 0020): the secret arrives in the URL fragment, is
// kept in memory only and goes back on every admin call in a header.

export const SESSION_HEADER = "Understudy-UI-Session";

export type Credentials = { readonly secret: string; readonly token: string | null };

// secretFromHash returns the s parameter of a location hash ("#s=<secret>"),
// or null when it is absent or empty.
export function secretFromHash(hash: string): string | null {
  const s = new URLSearchParams(hash.startsWith("#") ? hash.slice(1) : hash).get("s");
  return s === null || s === "" ? null : s;
}

// withoutFragment returns href up to, not including, its first "#".
export function withoutFragment(href: string): string {
  const at = href.indexOf("#");
  return at < 0 ? href : href.slice(0, at);
}

// headersFor returns the headers every admin call sends: the secret, and
// the admin token as a Bearer credential when there is one (ADR 0017).
export function headersFor(c: Credentials): Record<string, string> {
  return {
    [SESSION_HEADER]: c.secret,
    ...(c.token === null || c.token === "" ? {} : { Authorization: `Bearer ${c.token}` }),
  };
}
