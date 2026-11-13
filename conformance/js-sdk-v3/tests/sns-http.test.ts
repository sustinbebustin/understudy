// SNS HTTP/S delivery checked by an independent validator: AWS's own
// sns-validator 0.3.5 (npm, aws/aws-js-sns-message-validator) and openssl.
// Expected behavior: docs/research/sns-http-delivery.md 5.1 (the field lists
// AWS signs), 5.3 (what sns-validator checks) and 8.3 item 2 (understudy signs
// AWS's lists, never the validator's). run.sh starts understudy with a TLS
// listener and an allow-list naming the receiver this file starts.

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  CreateTopicCommand,
  GetSubscriptionAttributesCommand,
  PublishCommand,
  SNSClient,
  SubscribeCommand,
} from "@aws-sdk/client-sns";
import MessageValidator from "sns-validator";
import { clientConfig } from "../support/env";
import { assertLoopbackLink, eventually, uniq } from "../support/helpers";

/** The settings run.sh passes; the file cannot run without them. */
function setting(name: string): string {
  const value = process.env[name];
  if (value === undefined || value === "") {
    throw new Error(`conformance: ${name} is unset; run this file through run.sh, which starts the TLS listener and the receiver's certificate`);
  }
  return value;
}

const sns = new SNSClient(clientConfig());

type SnsMessage = Record<string, string | undefined>;

interface Post {
  path: string;
  headers: Record<string, string>;
  body: string;
}

const posts: Post[] = [];
let receiver: ReturnType<typeof Bun.serve> | undefined;
let work = "";
let tlsHost = "";
let ca = "";

beforeAll(() => {
  const [host, port] = setting("CONFORMANCE_SNS_RECEIVER").split(":");
  tlsHost = new URL(setting("CONFORMANCE_TLS_ENDPOINT")).host;
  ca = readFileSync(setting("NODE_EXTRA_CA_CERTS"), "utf8");
  receiver = Bun.serve({
    hostname: host,
    port: Number(port),
    tls: { cert: readFileSync(setting("CONFORMANCE_SNS_RECEIVER_CERT"), "utf8"), key: readFileSync(setting("CONFORMANCE_SNS_RECEIVER_KEY"), "utf8") },
    async fetch(req) {
      posts.push({ path: new URL(req.url).pathname, headers: Object.fromEntries(req.headers), body: await req.text() });
      return new Response("");
    },
  });
  work = mkdtempSync(join(tmpdir(), "sns-http-"));
});

afterAll(() => {
  receiver?.stop(true);
  if (work !== "") rmSync(work, { recursive: true, force: true });
});

/** The last POST of type to path, once it has arrived. */
async function received(path: string, type: string): Promise<SnsMessage> {
  const post = await eventually(`a ${type} at ${path}`, async () =>
    posts.filter((p) => p.path === path && p.headers["x-amz-sns-message-type"] === type).at(-1),
  );
  return JSON.parse(post.body) as SnsMessage;
}

/** An unsigned GET of a loopback SNS link, trusting understudy's TLS certificate. */
async function follow(link: string): Promise<number> {
  assertLoopbackLink(link);
  const resp = await fetch(link, { tls: { ca } });
  await resp.text();
  return resp.status;
}

/** sns-validator's verdict: null when the message validates, else the error's message. */
function validate(message: SnsMessage): Promise<string | null> {
  // The host pattern includes the port, as sns-validator tests the URL's host
  // with its port (sns-http-delivery.md 5.3).
  const escaped = tlsHost.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const validator = new MessageValidator(new RegExp(`^${escaped}$`));
  return new Promise((resolve) => validator.validate({ ...message }, (err) => resolve(err === null ? null : err.message)));
}

/**
 * openssl's verdict on a message's signature over fields, each "name\nvalue\n"
 * when present, with the public key of the certificate at SigningCertURL: the
 * verify recipe of sns-http-delivery.md 9.3.
 */
async function opensslVerify(message: SnsMessage, fields: string[]): Promise<string> {
  const certURL = message.SigningCertURL ?? "";
  assertLoopbackLink(certURL);
  const resp = await fetch(certURL, { tls: { ca } });
  expect(resp.status).toBe(200);
  const dir = mkdtempSync(join(work, "verify-"));
  writeFileSync(join(dir, "cert.pem"), await resp.text());
  const pub = Bun.spawnSync(["openssl", "x509", "-pubkey", "-noout", "-in", join(dir, "cert.pem")]);
  expect(pub.exitCode).toBe(0);
  writeFileSync(join(dir, "pub.pem"), pub.stdout);
  writeFileSync(join(dir, "sig.bin"), Buffer.from(message.Signature ?? "", "base64"));
  const signed = fields
    .filter((f) => message[f] !== undefined && message[f] !== null)
    .map((f) => `${f}\n${message[f]}\n`)
    .join("");
  writeFileSync(join(dir, "signed.txt"), signed);
  // SignatureVersion 1 is SHA1withRSA, 2 SHA256withRSA (sns-http-delivery.md 5.2).
  const alg = message.SignatureVersion === "2" ? "-sha256" : "-sha1";
  const out = Bun.spawnSync(["openssl", "dgst", alg, "-verify", join(dir, "pub.pem"), "-signature", join(dir, "sig.bin"), join(dir, "signed.txt")]);
  return (out.stdout.toString() + out.stderr.toString()).trim();
}

// The field lists AWS signs (sns-http-delivery.md 5.1, aws-doc verification
// steps, third-party-recorded against real AWS signatures).
const notificationFields = ["Message", "MessageId", "Subject", "Timestamp", "TopicArn", "Type"];
const confirmationFields = ["Message", "MessageId", "SubscribeURL", "Timestamp", "Token", "TopicArn", "Type"];

/** AWS's DNS suffixes (sns-http-delivery.md 7.3); none may appear in what understudy sends (8.8). */
const awsHost = /amazonaws\.com|amazonaws\.eu|amazonwebservices|api\.aws|c2s\.ic\.gov|sc2s\.sgov\.gov|aws\.scloud|adc-e\.uk|hci\.ic\.gov|\.aws(?:[/:?"\\\s]|$)/i;

describe.each(["1", "2"])("SNS HTTPS delivery under SignatureVersion %s", (version) => {
  const path = `/sns-http-v${version}-${uniq("r")}`;
  const messages: Record<string, SnsMessage> = {};

  test("the handshake reaches the receiver", async () => {
    const topic = await sns.send(new CreateTopicCommand({ Name: uniq("t"), Attributes: { SignatureVersion: version } }));
    const topicArn = topic.TopicArn ?? "";
    const sub = await sns.send(
      new SubscribeCommand({ TopicArn: topicArn, Protocol: "https", Endpoint: `https://${setting("CONFORMANCE_SNS_RECEIVER")}${path}`, ReturnSubscriptionArn: true }),
    );
    const arn = sub.SubscriptionArn ?? "";

    messages.SubscriptionConfirmation = await received(path, "SubscriptionConfirmation");
    expect(await follow(messages.SubscriptionConfirmation.SubscribeURL ?? "")).toBe(200);
    await eventually("the subscription to be confirmed", async () => {
      const attrs = await sns.send(new GetSubscriptionAttributesCommand({ SubscriptionArn: arn }));
      return attrs.Attributes?.PendingConfirmation === "false" ? true : undefined;
    });

    await sns.send(new PublishCommand({ TopicArn: topicArn, Message: `hello ${version}`, Subject: "greeting" }));
    messages.Notification = await received(path, "Notification");
    expect(messages.Notification.Message).toBe(`hello ${version}`);

    expect(await follow(messages.Notification.UnsubscribeURL ?? "")).toBe(200);
    messages.UnsubscribeConfirmation = await received(path, "UnsubscribeConfirmation");
    for (const m of Object.values(messages)) expect(m.SignatureVersion).toBe(version);
  });

  // sns-validator 0.3.5 accepts a Notification and a SubscriptionConfirmation
  // whose certificate it fetches from an accepted host (5.3, 5.6).
  test("sns-validator validates the Notification and the SubscriptionConfirmation", async () => {
    expect(await validate(messages.Notification)).toBeNull();
    expect(await validate(messages.SubscriptionConfirmation)).toBeNull();
  });

  // AWS signs an UnsubscribeConfirmation with its Token; sns-validator checks
  // it without, so it rejects every genuine one (5.1, 5.3), and so this one.
  test("sns-validator rejects the UnsubscribeConfirmation, which openssl verifies over AWS's list", async () => {
    expect(await validate(messages.UnsubscribeConfirmation)).toBe("The message signature is invalid.");
    expect(await opensslVerify(messages.UnsubscribeConfirmation, confirmationFields)).toBe("Verified OK");
    // sns-validator's list and the format page's list do not verify (9.3, lines 5 and 6).
    expect(await opensslVerify(messages.UnsubscribeConfirmation, ["Message", "MessageId", "Subject", "SubscribeURL", "Timestamp", "TopicArn", "Type"])).not.toBe("Verified OK");
    expect(await opensslVerify(messages.UnsubscribeConfirmation, ["Message", "MessageId", "Timestamp", "TopicArn", "Type"])).not.toBe("Verified OK");
  });

  test("openssl verifies the Notification and the SubscriptionConfirmation over AWS's lists", async () => {
    expect(await opensslVerify(messages.Notification, notificationFields)).toBe("Verified OK");
    expect(await opensslVerify(messages.SubscriptionConfirmation, confirmationFields)).toBe("Verified OK");
  });

  test("no AWS host appears in any header or body the receiver got", () => {
    const mine = posts.filter((p) => p.path === path);
    expect(mine.length).toBeGreaterThanOrEqual(3);
    for (const p of mine) {
      expect(JSON.stringify(p.headers)).not.toMatch(awsHost);
      expect(p.body).not.toMatch(awsHost);
    }
  });
});
