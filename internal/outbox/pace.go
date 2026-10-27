package outbox

import (
	"sync"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
)

// pacer spaces the attempts of throttled SNS http and https subscriptions:
// under throttlePolicy.maxReceivesPerSecond, the attempts of one
// subscription start at least SNSHTTP.PaceInterval apart on the emulator
// clock, and a paced attempt waits "without consuming retry attempts"
// (sns-http-delivery.md 6.4, aws-doc; 8.2 item 5). An attempt whose timer
// fires early is re-armed for a slot of its own: the first instant after
// the subscription's last start and every slot already handed out, so each
// paced attempt is re-armed once and they start in the order they were
// paced.
//
// A throttled subscription's attempts also run one at a time, in the
// order they start, on a sequencer of its own, so the journal records them
// in that order even when one clock advance starts several. Whether AWS
// overlaps the attempts of a throttled subscription is UNVERIFIED, as is
// the pacing it actually applies (SNSH-20).
//
// The start instants and slots are this process's: a reset or a snapshot
// load forgets them, so the next attempt of each subscription starts
// unpaced.
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

// throttled is one throttled subscription. Its sequencer is kept while the
// subscription is paced, so two drains never run its attempts at once.
type throttled struct {
	// last is the latest attempt start; started is false until there is
	// one, and again after a reset or load forgets it.
	last    clock.Instant
	started bool
	// next is the first slot no paced attempt holds, and slots the slot
	// each paced attempt holds, by delivery.
	next  clock.Instant
	slots map[ids.DeliveryID]clock.Instant
	ready sequencer
}

// admit decides when the attempt of en due at due starts. paced is true
// when it comes less than its policy's PaceInterval after its
// subscription's last start, or before a slot an attempt paced earlier
// holds, and rearm is the slot it now holds. Otherwise due is recorded as
// that start and ready is the sequencer the attempt runs on; ready is nil
// for an attempt under no throttle.
func (p *pacer) admit(en *Entry, due clock.Instant) (rearm clock.Instant, paced bool, ready *sequencer) {
	policy, isHTTPPolicy := en.Request.Policy.(delivery.SNSHTTP)
	endpoint, isHTTP := en.Request.Target.(delivery.HTTPEndpoint)
	interval := policy.PaceInterval()
	if !isHTTPPolicy || !isHTTP || interval <= 0 {
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
	slot, holds := sub.slots[en.ID]
	holds = holds && slot.Compare(due) == 0
	delete(sub.slots, en.ID)
	allowed := sub.last.Add(interval)
	free := laterOf(allowed, sub.next)
	if !holds {
		// The slots before next belong to attempts paced earlier.
		allowed = free
	}
	if sub.started && due.Compare(allowed) < 0 {
		if sub.slots == nil {
			sub.slots = map[ids.DeliveryID]clock.Instant{}
		}
		sub.slots[en.ID] = free
		sub.next = free.Add(interval)
		return free, true, nil
	}
	sub.last, sub.started = due, true
	return clock.Instant{}, false, &sub.ready
}

// laterOf returns the later of a and b.
func laterOf(a, b clock.Instant) clock.Instant {
	if a.Compare(b) < 0 {
		return b
	}
	return a
}

// forget drops the last start and the slots of every subscription whose
// topic keep rejects, and the subscription itself once its sequencer is
// idle, so the pacer holds only the subscriptions still paced.
func (p *pacer) forget(keep func(aws.Scope) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, sub := range p.subs {
		if scope, err := aws.ScopeOf(key.source); err == nil && keep(scope) {
			continue
		}
		if sub.ready.idle() {
			delete(p.subs, key)
			continue
		}
		sub.started, sub.next, sub.slots = false, clock.Instant{}, nil
	}
}
