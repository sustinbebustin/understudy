package config

import (
	"encoding/json"
	"maps"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/fault"
)

// applySections applies the file's structured settings; name is the file
// name errors start with.
func (d *draft) applySections(s fileSections, name string) *Error {
	steps := []func() *Error{
		func() *Error { return d.applyAccounts(s.Accounts, name+": accounts") },
		func() *Error { return d.applyRegions(s.Regions, name+": regions") },
		func() *Error { return d.applyS3(s.S3, name+": s3") },
		func() *Error { return d.applyLambda(s.Lambda, name+": lambda") },
		func() *Error { return d.applyDynamoDB(s.DynamoDB, name+": dynamodb") },
		func() *Error { return d.applyJournal(s.Journal, name+": journal") },
		func() *Error { return d.applyFaults(s.Faults, name+": faults") },
	}
	if s.SQS != nil && s.SQS.QueueDeletedRecently != nil {
		steps = append(steps, func() *Error {
			return choose(*s.SQS.QueueDeletedRecently, name+": sqs.queueDeletedRecently",
				map[string]DeletedRecently{"ignore": DeletedRecentlyIgnore, "enforce": DeletedRecentlyEnforce}, &d.cfg.SQS.DeletedRecently)
		})
	}
	if s.IAM != nil && s.IAM.AssumeRole != nil {
		steps = append(steps, func() *Error {
			return choose(*s.IAM.AssumeRole, name+": iam.assumeRole",
				map[string]AssumeRole{"strict": AssumeRoleStrict, "lenient": AssumeRoleLenient}, &d.cfg.IAM.AssumeRole)
		})
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// choose sets *dst to the value text names among values.
func choose[T any](text, where string, values map[string]T, dst *T) *Error {
	v, ok := values[text]
	if !ok {
		// Sorted, so closest breaks a tie the same way on every run.
		names := slices.Sorted(maps.Keys(values))
		return &Error{Where: where, Reason: quote(text) + " is not a value it takes", Closest: Closest(text, names), Hint: "it takes " + strings.Join(names, " or ")}
	}
	*dst = v
	return nil
}

func (d *draft) applyAccounts(a *fileAccounts, where string) *Error {
	if a == nil {
		return nil
	}
	if a.Default != nil {
		acct, err := aws.ParseAccountID(*a.Default)
		if err != nil {
			return &Error{Where: where + ".default", Reason: quote(*a.Default) + " is not a 12-digit account id", Hint: "write it as 000000000000"}
		}
		d.cfg.Accounts.Default = acct
	}
	seen := map[string]bool{}
	for i, k := range a.Keys {
		at := where + ".keys[" + strconv.Itoa(i) + "]"
		if !validAccessKeyID(k.AccessKeyID) {
			return &Error{Where: at + ".accessKeyId", Reason: quote(k.AccessKeyID) + " is not 16 to 128 letters and digits", Hint: "DynamoDB Local accepts only alphanumeric keys"}
		}
		if seen[k.AccessKeyID] {
			return &Error{Where: at + ".accessKeyId", Reason: quote(k.AccessKeyID) + " is listed twice"}
		}
		seen[k.AccessKeyID] = true
		if k.SecretAccessKey == "" {
			return &Error{Where: at + ".secretAccessKey", Reason: "is missing or empty"}
		}
		acct, err := aws.ParseAccountID(k.Account)
		if err != nil {
			return &Error{Where: at + ".account", Reason: quote(k.Account) + " is not a 12-digit account id"}
		}
		d.cfg.Accounts.Keys = append(d.cfg.Accounts.Keys, StaticKey{AccessKeyID: k.AccessKeyID, Secret: Secret(k.SecretAccessKey), Account: acct})
	}
	return nil
}

func validAccessKeyID(id string) bool {
	if len(id) < 16 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func (d *draft) applyRegions(names []string, where string) *Error {
	if len(names) == 0 {
		return nil
	}
	table, err := aws.KnownRegions().WithExtra(names...)
	if err != nil {
		return &Error{Where: where, Reason: err.Error(), Hint: "region names are lowercase letters, digits and inner hyphens, such as local-1"}
	}
	for _, n := range names {
		r, err := table.Parse(n)
		if err != nil {
			return &Error{Where: where, Reason: err.Error()}
		}
		d.cfg.Regions = append(d.cfg.Regions, r)
	}
	return nil
}

func (d *draft) applyS3(s *fileS3, where string) *Error {
	if s == nil {
		return nil
	}
	for i, domain := range s.BaseDomains {
		if !allowedBaseDomain(domain) {
			return &Error{
				Where:  where + ".baseDomains[" + strconv.Itoa(i) + "]",
				Reason: quote(domain) + " does not resolve to loopback by itself",
				Hint:   "use a name under localhost, such as s3.dev.localhost, or localhost.localstack.cloud for LocalStack migrations",
			}
		}
		d.cfg.S3.BaseDomains = append(d.cfg.S3.BaseDomains, domain)
	}
	if s.CrossRegion != nil {
		if err := choose(*s.CrossRegion, where+".crossRegion", map[string]CrossRegion{"aws": CrossRegionAWS, "lenient": CrossRegionLenient}, &d.cfg.S3.CrossRegion); err != nil {
			return err
		}
	}
	if s.MaxObjectBytes != nil {
		if *s.MaxObjectBytes <= 0 || *s.MaxObjectBytes > DefaultMaxObjectBytes {
			return &Error{Where: where + ".maxObjectBytes", Reason: strconv.FormatInt(*s.MaxObjectBytes, 10) + " is not between 1 and 5 GiB (5368709120), S3's single PUT limit"}
		}
		d.cfg.S3.MaxObjectBytes = *s.MaxObjectBytes
	}
	return nil
}

// allowedBaseDomain reports whether domain is localhost, a name under it
// (RFC 6761 resolves both to loopback), or localhost.localstack.cloud,
// LocalStack's public name for 127.0.0.1 (ADR 0013).
func allowedBaseDomain(domain string) bool {
	if domain == "localhost.localstack.cloud" || domain == "localhost" {
		return true
	}
	labels, ok := strings.CutSuffix(domain, ".localhost")
	if !ok || labels == "" {
		return false
	}
	for label := range strings.SplitSeq(labels, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func (d *draft) applyLambda(l *fileLambda, where string) *Error {
	if l == nil {
		return nil
	}
	if err := d.applyNetwork(l, where); err != nil {
		return err
	}
	if err := positive(l.StartConcurrency, where+".startConcurrency", &d.cfg.Lambda.StartConcurrency); err != nil {
		return err
	}
	if err := positive(l.MaxEnvironments, where+".maxEnvironments", &d.cfg.Lambda.MaxEnvironments); err != nil {
		return err
	}
	if l.IdleTimeout != nil {
		t, err := time.ParseDuration(*l.IdleTimeout)
		if err != nil || t < time.Second {
			return &Error{Where: where + ".idleTimeout", Reason: quote(*l.IdleTimeout) + " is not a duration of at least 1s", Hint: "write it as 10m"}
		}
		d.cfg.Lambda.IdleTimeout = t
	}
	if l.StateTransitions != nil {
		if err := choose(*l.StateTransitions, where+".stateTransitions", map[string]StateTransitions{"final": TransitionsFinal, "clock": TransitionsClock}, &d.cfg.Lambda.StateTransitions); err != nil {
			return err
		}
	}
	for i, h := range l.HotReload {
		m, err := d.hotReload(h, where+".hotReload["+strconv.Itoa(i)+"]")
		if err != nil {
			return err
		}
		d.cfg.Lambda.HotReload = append(d.cfg.Lambda.HotReload, m)
	}
	return nil
}

// hotReload parses one mapping; its region may be one of the configured
// extra regions, which applyRegions has already read.
func (d *draft) hotReload(h fileHotReload, at string) (HotReload, *Error) {
	switch {
	case h.Function == "":
		return HotReload{}, &Error{Where: at + ".function", Reason: "is missing; name the function whose code the directory replaces"}
	case !filepath.IsAbs(h.Dir):
		return HotReload{}, &Error{Where: at + ".dir", Reason: quote(h.Dir) + " is not an absolute path", Hint: "write the directory's absolute host path"}
	}
	m := HotReload{Function: h.Function, Dir: h.Dir}
	if h.Account != "" {
		acct, err := aws.ParseAccountID(h.Account)
		if err != nil {
			return HotReload{}, &Error{Where: at + ".account", Reason: quote(h.Account) + " is not a 12-digit account id", Hint: "write it as 000000000000, or remove it for the default account"}
		}
		m.Account = acct
	}
	if h.Region != "" {
		extra := make([]string, len(d.cfg.Regions))
		for i, r := range d.cfg.Regions {
			extra[i] = r.String()
		}
		table, err := aws.KnownRegions().WithExtra(extra...)
		if err == nil {
			m.Region, err = table.Parse(h.Region)
		}
		if err != nil {
			return HotReload{}, &Error{Where: at + ".region", Reason: err.Error(), Hint: "name an AWS region such as us-east-1, or add it to regions"}
		}
	}
	return m, nil
}

func (d *draft) applyNetwork(l *fileLambda, where string) *Error {
	network := "auto"
	if l.Network != nil {
		network = *l.Network
	}
	if l.BridgeGateway != nil && network != "bridge" {
		return &Error{Where: where + ".bridgeGateway", Reason: "applies only to network bridge", Hint: `set "network": "bridge" or remove the gateway`}
	}
	switch network {
	case "auto":
		d.cfg.Lambda.Network = NetworkAuto{}
	case "disabled":
		d.cfg.Lambda.Network = NetworkDisabled{}
	case "bridge":
		if l.BridgeGateway == nil {
			return &Error{Where: where + ".bridgeGateway", Reason: "is missing; network bridge binds the container listener on this address", Hint: "write it as 172.17.0.1"}
		}
		gw, reason := parseBridgeGateway(*l.BridgeGateway)
		if reason != "" {
			return &Error{
				Where:  where + ".bridgeGateway",
				Reason: quote(*l.BridgeGateway) + " " + reason,
				Hint:   "write the gateway `docker network inspect bridge` reports, such as 172.17.0.1, or set \"network\": \"auto\" to detect it",
			}
		}
		d.cfg.Lambda.Network = NetworkBridge{Gateway: gw}
	default:
		return &Error{Where: where + ".network", Reason: quote(network) + " is not auto, bridge or disabled", Closest: Closest(network, []string{"auto", "bridge", "disabled"})}
	}
	return nil
}

// parseBridgeGateway reads a bridge gateway; the reason is empty when it is
// valid. The container listener binds the gateway and serves the AWS API to
// any client signing with the public dummy pair test/test, which verifies
// even under --sigv4 verify, and no listener rule guards it, so only a
// private (RFC 1918) IPv4 address, where Docker puts its bridges, passes.
func parseBridgeGateway(text string) (gw netip.Addr, reason string) {
	gw, err := netip.ParseAddr(text)
	switch {
	case err != nil || !gw.Is4():
		return netip.Addr{}, "is not an IPv4 address"
	case gw.IsUnspecified():
		return netip.Addr{}, "is the unspecified address, which would bind the container listener on every interface and open its unauthenticated AWS API to the network"
	case gw.IsLoopback():
		return netip.Addr{}, "is a loopback address, which no container on a bridge network reaches"
	case !gw.IsPrivate():
		return netip.Addr{}, "is not a private (RFC 1918) address; the container listener binds it and serves the AWS API without authentication, so a public address would open that API beyond this machine"
	}
	return gw, ""
}

func positive(n *int, where string, dst *int) *Error {
	if n == nil {
		return nil
	}
	if *n <= 0 {
		return &Error{Where: where, Reason: strconv.Itoa(*n) + " is not a positive integer"}
	}
	*dst = *n
	return nil
}

func (d *draft) applyDynamoDB(s *fileDynamoDB, where string) *Error {
	if s == nil {
		return nil
	}
	if s.Enabled != nil && !*s.Enabled {
		d.dynamoOff = true
	}
	if s.Registry != nil {
		if err := choose(*s.Registry, where+".registry", map[string]Registry{"docker-hub": RegistryDockerHub, "ecr-public": RegistryECRPublic}, &d.cfg.DynamoDB.Registry); err != nil {
			return err
		}
	}
	if s.MemoryLimit != nil {
		if *s.MemoryLimit < 256<<20 {
			return &Error{Where: where + ".memoryLimit", Reason: strconv.FormatInt(*s.MemoryLimit, 10) + " is below 268435456 bytes (256 MiB), too little for DynamoDB Local's JVM"}
		}
		d.cfg.DynamoDB.MemoryLimit = *s.MemoryLimit
	}
	return nil
}

func (d *draft) applyJournal(j *fileJournal, where string) *Error {
	if j == nil {
		return nil
	}
	if err := positive(j.Capacity, where+".capacity", &d.cfg.Journal.Capacity); err != nil {
		return err
	}
	if err := positive(j.MaxBytes, where+".maxBytes", &d.cfg.Journal.MaxBytes); err != nil {
		return err
	}
	if j.File != nil {
		if *j.File == "" {
			return &Error{Where: where + ".file", Reason: "is empty", Hint: "give a path or remove the key"}
		}
		d.cfg.Journal.File = *j.File
	}
	return nil
}

func (d *draft) applyFaults(rules []json.RawMessage, where string) *Error {
	for i, raw := range rules {
		var r fault.Rule
		if err := r.UnmarshalJSON(raw); err != nil {
			return &Error{Where: where + "[" + strconv.Itoa(i) + "]", Reason: err.Error(), Hint: `each rule takes the form of a "rule" entry that ` + "`understudy faults list --output json`" + ` prints`}
		}
		d.cfg.Faults = append(d.cfg.Faults, r)
	}
	return nil
}
