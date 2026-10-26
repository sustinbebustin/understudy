// Types for sns-validator 0.3.5 (aws/aws-js-sns-message-validator), which
// ships none: its index.js exports the MessageValidator constructor.
declare module "sns-validator" {
  export default class MessageValidator {
    constructor(hostPattern?: RegExp, encoding?: string);
    validate(
      message: string | Record<string, unknown>,
      cb: (err: Error | null, message?: Record<string, unknown>) => void,
    ): void;
  }
}
