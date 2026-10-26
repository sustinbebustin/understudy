package outbox

import (
	"sync"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
)

// pacer spaces the attempts of throttled SNS http and https subscriptions:
// under throttlePolicy.maxReceivesPerSecond, the attempts of one
// subscription start at least SNSHTTP.PaceInterval apart on the emulator
// clock, and a paced attempt waits "without consuming retry attempts"
// (sns-http-delivery.md 6.4, aws-doc; 8.2 item 5). An attempt whose timer
// fires early is re-armed for the instant its subscription's last start
// allows.
//
// A throttled subscription's attempts also run one at a time, in the
// order they start, on a sequencer of its own, so the journal records them
// in that order even when one clock advance starts several. Whether AWS
// overlaps the attempts of a throttled subscription is UNVERIFIED, as is
// the pacing it actually applies (SNSH-20).
//
// The start instants are this process's: a reset or a snapshot load
// forgets them, so the next attempt of each subscription starts unpaced.
type pacer struct {
	mu   sync.Mutex
	subs map[paceKey]*throttled
}

// paceKey names a subscription by its topic and endpoint URL: a topic holds
// one subscription per URL.
type paceKey struct {
	source aws.ARN
	url    string
}

// throttled is one throttled subscription. Its sequencer is kept for the
// life of the process, so two drains never run its attempts at once.
type throttled struct {
	// last is the latest attempt start; started is false until there is
	// one, and again after a reset or load forgets it.
	last    clock.Instant
	started bool
	ready   sequencer
}

// admit decides when the attempt of en due at due starts. paced is true
// when it comes less than its policy's PaceInterval after its
// subscription's last start, and rearm is the instant it may start.
// Otherwise due is recorded as that start and ready is the sequencer the
// attempt runs on; ready is nil for an attempt under no throttle.
func (p *pacer) admit(en *Entry, due clock.Instant) (rearm clock.Instant, paced bool, ready *sequencer) {
	policy, isHTTPPolicy := en.Request.Policy.(delivery.SNSHTTP)
	endpoint, isHTTP := en.Request.Target.(delivery.HTTPEndpoint)
	if !isHTTPPolicy || !isHTTP || policy.PaceInterval() <= 0 {
		return clock.Instant{}, false, nil
	}
	key := paceKey{source: en.Request.Source, url: endpoint.URL.String()}
	p.mu.Lock()
	defer p.mu.Unlock()
	sub, ok := p.subs[key]
	if !ok {
		if p.subs == nil {
			p.subs = map[paceKey]*throttled{}
		}
		sub = &throttled{}
		p.subs[key] = sub
	}
	if allowed := sub.last.Add(policy.PaceInterval()); sub.started && due.Compare(allowed) < 0 {
		return allowed, true, nil
	}
	sub.last, sub.started = due, true
	return clock.Instant{}, false, &sub.ready
}

// forget drops the last start of every subscription whose topic keep
// rejects.
func (p *pacer) forget(keep func(aws.Scope) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, sub := range p.subs {
		if scope, err := aws.ScopeOf(key.source); err != nil || !keep(scope) {
			sub.started = false
		}
	}
}
