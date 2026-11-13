// The guard every SNS link follower runs before its GET, so a wrong link can
// never send a request off the machine (8.8).

import { describe, expect, test } from "bun:test";
import { assertLoopbackLink } from "../support/helpers";

describe("assertLoopbackLink", () => {
  test.each([
    "http://127.0.0.1:45663/x",
    "https://127.0.0.1:40075/?Action=ConfirmSubscription",
    "http://localhost:8080/x",
    "http://LOCALHOST:8080/x",
    "http://sns.localhost:8080/x",
    "http://[::1]:8080/x",
  ])("accepts %s", (link) => {
    expect(() => assertLoopbackLink(link)).not.toThrow();
  });

  test.each([
    "https://sns.us-east-1.amazonaws.com/?Action=ConfirmSubscription",
    "http://example.com:8080/x",
    "http://10.0.0.1:8080/x",
    "http://notlocalhost:8080/x",
    "http://localhost.example.com/x",
    "not a url",
    "",
  ])("refuses %p and names the link", (link) => {
    expect(() => assertLoopbackLink(link)).toThrow(link === "" ? "[]" : link);
  });
});
