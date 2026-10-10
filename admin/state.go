package admin

import (
	"encoding/json"
	"time"
)

// SnapshotInfo describes an archive the server saved to or loaded from.
type SnapshotInfo struct {
	// Path is the archive's path under the data directory, for the path
	// form; empty when the archive travelled in the request or response.
	Path string `json:"path,omitempty"`
	// Bytes is the archive's size, for a save to a path.
	Bytes int64 `json:"bytes,omitempty"`
	// Writer names the build that wrote the archive and ModelPin its
	// vendored Smithy models.
	Writer   string `json:"writer"`
	ModelPin string `json:"modelPin,omitempty"`
	// Clock is emulator time and mode at the cut.
	Clock    time.Time `json:"clock"`
	Mode     ClockMode `json:"mode"`
	Sections int       `json:"sections"`
	Services []string  `json:"services,omitempty"`
}

// SnapshotRequest is the JSON body of the path form of POST and PUT
// /snapshots, which needs persistence (serve --persist DIR).
type SnapshotRequest struct {
	// Path is relative to the data directory.
	Path string `json:"path"`
}

// SaveOptions shapes an archive POST /snapshots writes.
type SaveOptions struct {
	Gzip bool
	// IncludeJournal adds the retained journal as journal.ndjson.
	IncludeJournal bool
}

// Resource is one resource GET /resources lists.
type Resource struct {
	Service string `json:"service"`
	Account string `json:"account"`
	Region  string `json:"region"`
	ARN     string `json:"arn"`
	// Summary is a short state description, such as "3 messages".
	Summary string `json:"summary,omitempty"`
}

// ResourceQuery selects resources; empty lists select everything.
type ResourceQuery struct {
	Services []string
	Accounts []string
	Regions  []string
}

// Usage is one recorded use of an IAM action: one principal, in one mode,
// on one resource.
type Usage struct {
	Principal string `json:"principal"`
	// Mode is whom AWS would authorize the call as: "direct", "caller",
	// "execution-role" or "service".
	Mode   string `json:"mode"`
	Action string `json:"action"`
	// Resource is the ARN the action was called on, or "*" for an action
	// without a resource of its service.
	Resource string    `json:"resource"`
	Calls    int       `json:"calls"`
	Failures int       `json:"failures,omitempty"`
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
}

// UsageQuery selects uses; zero fields select everything.
type UsageQuery struct {
	// Principal is an IAM ARN or a service principal name.
	Principal string
	// Service is an IAM action prefix, such as "sqs".
	Service string
}

// Policy is GET /iam/policy: the least-privilege identity policy for a
// principal, and the resource-policy statements recorded deliveries would
// need in AWS.
type Policy struct {
	Principal string `json:"principal"`
	// Document is the IAM policy document.
	Document         json.RawMessage  `json:"document"`
	ResourcePolicies []ResourcePolicy `json:"resourcePolicies,omitempty"`
}

// ResourcePolicy is a statement the resource policy of Target must hold:
// allow Principal to call Action from Source.
type ResourcePolicy struct {
	Target    string `json:"target"`
	Principal string `json:"principal"`
	Action    string `json:"action"`
	Source    string `json:"source"`
}

// Settle is GET /settle once no tracked work is pending.
type Settle struct {
	Settled bool `json:"settled"`
}

// HotReloadRequest is the body of POST /lambda/functions/{name}/hot-reload.
// Account and Region default to the server's default account and region.
type HotReloadRequest struct {
	// Dir is the host directory whose files replace the function's code.
	Dir     string `json:"dir"`
	Account string `json:"account,omitempty"`
	Region  string `json:"region,omitempty"`
}

// ImagePullRequest is the body of POST /lambda/images/pull; empty Runtimes
// pulls every pinned runtime image.
type ImagePullRequest struct {
	Runtimes []string `json:"runtimes,omitempty"`
}

// ImagePullResult names the images pulled.
type ImagePullResult struct {
	Images []string `json:"images"`
}

// TTLSweepResult is POST /dynamodb/ttl-sweep: the items the sweep deleted.
type TTLSweepResult struct {
	Deleted int `json:"deleted"`
}

// SubscriptionToken is GET /sns/subscriptions/{arn}/token: the confirmation
// token of a pending subscription.
type SubscriptionToken struct {
	Subscription string `json:"subscription"`
	Token        string `json:"token"`
}
