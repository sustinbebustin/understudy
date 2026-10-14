// Connection settings shared by every test file.
//
// The suite talks only to a loopback endpoint. run.sh starts understudy and
// launches bun through scripts/sealed, which sets AWS_ENDPOINT_URL and dummy
// credentials; running `bun test` by hand works the same once those are set,
// and falls back to the run.sh defaults otherwise.

const DEFAULT_ENDPOINT = "http://127.0.0.1:45663";

/** Throws unless raw is an http URL whose host is loopback. */
function requireLoopback(raw: string): URL {
  const url = new URL(raw);
  const host = url.hostname;
  const loopback =
    host === "localhost" ||
    host.endsWith(".localhost") ||
    host === "127.0.0.1" ||
    host === "[::1]";
  if (url.protocol !== "http:" || !loopback) {
    throw new Error(
      `conformance: refusing endpoint ${raw}: the suite only talks to a loopback http endpoint ` +
        `such as ${DEFAULT_ENDPOINT}, so it can never reach real AWS. ` +
        `Start it through run.sh, or set AWS_ENDPOINT_URL to a loopback URL.`,
    );
  }
  return url;
}

const endpointUrl = requireLoopback(process.env.AWS_ENDPOINT_URL ?? DEFAULT_ENDPOINT);

/** The emulator endpoint without a trailing slash, e.g. http://127.0.0.1:45663. */
export const endpoint = endpointUrl.origin;

/** The emulator port. */
export const port = Number(endpointUrl.port);

/** Same listener, addressed as localhost so S3 clients choose virtual-hosted style. */
export const localhostEndpoint = `http://localhost:${port}`;

export const region = process.env.AWS_REGION ?? "us-east-1";

/** The default account's access key; understudy derives account 000000000000 from it. */
export const defaultAccessKeyId = "test";
export const defaultAccount = "000000000000";

export function credentialsFor(accessKeyId: string): { accessKeyId: string; secretAccessKey: string } {
  return { accessKeyId, secretAccessKey: "test" };
}

/** Client options every service client in the suite is built with. */
export function clientConfig(
  overrides: { endpoint?: string; region?: string; accessKeyId?: string } = {},
) {
  return {
    endpoint: overrides.endpoint ?? endpoint,
    region: overrides.region ?? region,
    credentials: credentialsFor(overrides.accessKeyId ?? defaultAccessKeyId),
    maxAttempts: 1,
  };
}

/** Whether the caller opted DynamoDB out of this run (CONFORMANCE_DYNAMODB=0). */
export const dynamoDbOptedOut = process.env.CONFORMANCE_DYNAMODB === "0";

/**
 * Whether DynamoDB runs: the caller accepted the DynamoDB Local licence for this run and did not
 * opt DynamoDB out. run.sh refuses to start until exactly one of UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1
 * and CONFORMANCE_DYNAMODB=0 is set; under CONFORMANCE_DYNAMODB=0 run.sh's summary gives each
 * skipped DynamoDB test the reason "DynamoDB opted out (CONFORMANCE_DYNAMODB=0)".
 */
export const dynamoDbEnabled = process.env.UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE === "1" && !dynamoDbOptedOut;

/** Whether understudy verifies signatures (run.sh's SIGV4=verify): a key it holds no secret for is refused. */
export const sigv4Verify = process.env.CONFORMANCE_SIGV4 === "verify";

/** Whether Lambda execution tests (which pull runtime images) are enabled. */
export const lambdaExecutionEnabled = process.env.CONFORMANCE_LAMBDA_EXECUTION !== "0";
