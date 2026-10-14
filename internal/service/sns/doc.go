// Package sns implements Amazon SNS: topics, subscriptions, Publish and
// PublishBatch, and delivery to SQS and Lambda through the outbox
// (ARCHITECTURE.md 16, ADR 0015).
//
// State is one cell per account and region holding its topics by name and
// the RSA key its notifications are signed with. A topic keeps its
// subscriptions in creation order, which is the order Publish hands their
// deliveries to the outbox as Deliver effects, after each subscription's
// filter policy (package filterpolicy) admits the message.
//
// # Decisions the architecture and research leave open
//
//   - Links. SigningCertURL, UnsubscribeURL and a confirmation's
//     SubscribeURL point at the host the publishing client addressed,
//     under /_understudy/sns/ followed by the raw operation that serves
//     them: SigningCert/<partition>/<region>/<account>/<file>.pem,
//     UnsubscribeLink?Action=Unsubscribe&SubscriptionArn=<arn> and
//     ConfirmSubscriptionLink?Action=ConfirmSubscription&TopicArn=<arn>&Token=<token>.
//     The query keeps AWS's shape, so code that reads SubscriptionArn out
//     of the URL works. The links carry no credentials, so the ARN alone
//     says which account and region they act in.
//   - Confirmation. An sqs or lambda endpoint is confirmed at Subscribe
//     when its ARN's account is the topic owner's or the caller's; every
//     other subscription is pending with a 288-hex-character token that
//     confirms for 48 hours of emulator time, after which the subscription
//     is gone, as AWS deletes it: every read skips it and the next write
//     of its topic drops it, so no timer is needed. A pending sqs
//     subscription's queue gets a signed SubscriptionConfirmation; a FIFO
//     queue gets it with the subscription id as message group and its
//     MessageId as deduplication id, which no recording shows. Other
//     pending endpoints get their token through the admin API. http,
//     https, email and email-json subscriptions receive nothing, confirmed
//     or not, and Subscribe and Publish say so in their coverage notes.
//   - Filter policy quota. A Subscribe whose FilterPolicy would pass the
//     topic's 200 still creates the subscription, without the policy, and
//     answers FilterPolicyLimitExceeded, as AWS documents (sns.md 3.1).
//     The 10,000 per account is not enforced: state is per account and
//     region, so no topic write sees the account's count.
//   - Unsubscribing. A subscription confirmed with
//     AuthenticateOnUnsubscribe=true, or an sqs subscription its queue's
//     owner made, which AWS documents as setting it automatically, refuses
//     the unsigned UnsubscribeURL with InvalidParameter and an inferred
//     text.
//   - MaximumMessageSize above 262144 bytes limits the topic to 100 sqs
//     and lambda subscriptions; SetTopicAttributes and Subscribe refuse a
//     change that breaks it with InvalidParameter and an inferred text.
//   - Identifiers. Subscription ids and confirmation tokens are drawn from
//     the SNS message id stream of internal/ids, so a seeded run repeats
//     them; ids has no stream of their own.
//   - DeleteTopic detaches the topic's subscriptions: they receive
//     nothing and are not listed, but GetSubscriptionAttributes still
//     reads them, as AWS-recorded, and Unsubscribe removes them, for 48
//     hours of emulator time (the fidelity switch
//     sns.delete-topic.detached-retention).
//   - FIFO. A duplicate inside the five-minute window gets a fresh
//     MessageId and the topic's next SequenceNumber and is not delivered
//     (the fidelity switch sns.fifo.duplicate). The sequence counts per
//     topic, which is increasing per message group as AWS documents.
//   - Signing. The key is made from crypto/rand outside the cell lock on
//     the first publish to a standard topic of the scope, or the first
//     SubscriptionConfirmation, and persisted in snapshots; envelopes are
//     signed inside the write that delivers them, so the deliveries a call
//     produces are decided under one lock.
//   - PublishBatch fails the whole call on any entry's error and never
//     returns a Failed entry: no recording shows one (sns.md 4.4).
//
// Error texts are the recorded ones of sns.md unless the code marks them
// inferred.
package sns
