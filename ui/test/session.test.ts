// The UI's session bootstrap (ADR 0020): serve prints the UI URL with the
// secret in its fragment; the page reads it from location.hash, removes the
// fragment from the address bar, and sends the secret in a custom header on
// every admin call, plus the admin token as a Bearer credential where one
// is configured (ADR 0017).

import { describe, expect, test } from "bun:test";

import { headersFor, SESSION_HEADER, secretFromHash, withoutFragment } from "../src/session";

describe("session", () => {
  test("the header is the one the admin handler reads (admin.UISessionHeader in admin/ui.go)", () => {
    expect(SESSION_HEADER).toBe("Understudy-UI-Session");
  });

  test("the secret is the fragment's s parameter, when not empty", () => {
    const cases: readonly (readonly [string, string | null])[] = [
      ["#s=ABCDEFGHIJKLMNOPQRSTUVWX27", "ABCDEFGHIJKLMNOPQRSTUVWX27"],
      ["#x=1&s=ABC", "ABC"],
      ["", null],
      ["#", null],
      ["#x=1", null],
      ["#s=", null],
    ];
    for (const [hash, want] of cases) {
      expect(secretFromHash(hash)).toBe(want);
    }
  });

  test("removing the fragment keeps everything before the first #", () => {
    const cases: readonly (readonly [string, string])[] = [
      ["http://127.0.0.1:4566/_understudy/ui/#s=ABC", "http://127.0.0.1:4566/_understudy/ui/"],
      ["http://127.0.0.1:4566/_understudy/ui/", "http://127.0.0.1:4566/_understudy/ui/"],
      ["http://localhost:4566/_understudy/ui/?view=clock#s=A#B", "http://localhost:4566/_understudy/ui/?view=clock"],
    ];
    for (const [href, want] of cases) {
      expect(withoutFragment(href)).toBe(want);
    }
  });

  test("every admin call carries the secret, and the token only when there is one", () => {
    expect(headersFor({ secret: "S3", token: null })).toEqual({ "Understudy-UI-Session": "S3" });
    expect(headersFor({ secret: "S3", token: "" })).toEqual({ "Understudy-UI-Session": "S3" });
    expect(headersFor({ secret: "S3", token: "t0ken" })).toEqual({ "Understudy-UI-Session": "S3", Authorization: "Bearer t0ken" });
  });
});
