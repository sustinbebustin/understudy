// Kinesis fixtures shared by tests/kinesis.test.ts and the Kinesis mapping
// case of tests/lambda.test.ts.
//
// @aws-sdk/client-kinesis defaults to NodeHttp2Handler on Node, which opens a
// cleartext endpoint with an HTTP/2 prior-knowledge preface; understudy's
// listener speaks HTTP/1.1 and answers that preface with a 501 diagnostic
// (docs/research/kinesis.md 2.4, 13.4 item 3), so the suite passes an
// HTTP/1.1 handler, as a user must.

import { CreateStreamCommand, KinesisClient, waitUntilStreamExists } from "@aws-sdk/client-kinesis";
import { NodeHttpHandler } from "@smithy/node-http-handler";
import { clientConfig } from "./env";

export function kinesisClient(): KinesisClient {
  return new KinesisClient({ ...clientConfig(), requestHandler: new NodeHttpHandler() });
}

/** Creates a stream (PROVISIONED with shards, or with only its name) and waits until it is ACTIVE (kinesis.md 4.2). */
export async function createStream(client: KinesisClient, name: string, shards?: number): Promise<void> {
  await client.send(new CreateStreamCommand({ StreamName: name, ...(shards === undefined ? {} : { ShardCount: shards }) }));
  await waitUntilStreamExists({ client, maxWaitTime: 120, minDelay: 1 }, { StreamName: name });
}
