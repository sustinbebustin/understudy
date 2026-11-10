// Admin API answers: errors come as {"error": {"code", "message", "hint"}}
// with a closed code set (ADR 0017 and its amendment of 2026-10-08); a
// refused or missing admin token is code unauthorized with status 401 (ADR
// 0017, amendment 2026-10-13), which is when the page asks for the token.

import { describe, expect, test } from "bun:test";

import { adminError, needsToken } from "../src/api";

describe("adminError", () => {
  test("reads the envelope's error", () => {
    expect(adminError('{"error":{"code":"unavailable","message":"listing snapshots needs persistence","hint":"start serve with --persist DIR"}}')).toEqual({
      code: "unavailable",
      message: "listing snapshots needs persistence",
      hint: "start serve with --persist DIR",
    });
    expect(adminError('{"error":{"code":"not_found","message":"no rule r"}}')).toEqual({ code: "not_found", message: "no rule r" });
  });

  test("anything else is no admin error", () => {
    for (const body of ["", "<html>bad gateway</html>", "null", "[]", '{"events":[]}', '{"error":null}', '{"error":{"code":5,"message":"x"}}', '{"error":{"code":"internal"}}']) {
      expect(adminError(body)).toBeNull();
    }
  });
});

describe("needsToken", () => {
  test("only a 401 with code unauthorized asks for the token", () => {
    const unauthorized = { code: "unauthorized", message: "the admin API needs the token" };
    expect(needsToken(401, unauthorized)).toBe(true);
    expect(needsToken(403, unauthorized)).toBe(false);
    expect(needsToken(401, { code: "invalid_request", message: "x" })).toBe(false);
    expect(needsToken(401, null)).toBe(false);
  });
});
