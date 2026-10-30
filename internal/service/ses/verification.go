package ses

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TimerExpiry is the timer that fails a Pending identity at its deadline
// (ses.md 5.2, SES-27).
const TimerExpiry kernel.TimerKind = "ses.verification-expiry"

// expiryTimerID is the id of identity name's expiry timer: its ARN, which
// names the scope.
func expiryTimerID(scope aws.Scope, name string) string {
	return identityARN(scope, name).String()
}

// parseExpiryTimerID reads an expiryTimerID back.
func parseExpiryTimerID(id string) (aws.Scope, string, bool) {
	arn, err := aws.ParseARN(id)
	if err != nil {
		return aws.Scope{}, "", false
	}
	scope, err := aws.ScopeOf(arn)
	if err != nil {
		return aws.Scope{}, "", false
	}
	name, ok := strings.CutPrefix(arn.Resource, "identity/")
	return scope, name, ok
}

// onExpiry turns the timer's identity Failed when it is still Pending at
// its deadline: verification never completes on its own (ses.md 11.8), and
// the change is a StateChange on the identity ARN. An identity deleted,
// verified or restarted since the timer was armed is left alone.
func (s *Service) onExpiry(c *kernel.Call, key clock.TimerKey, _ clock.Instant) {
	scope, name, ok := parseExpiryTimerID(key.ID)
	if !ok {
		return
	}
	if _, ok := s.cells.Lookup(scope); !ok {
		return
	}
	_, err := kernel.Write(c, s.cells, scope, func(st *state, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		i := st.find(name)
		if i < 0 {
			tx.Unchanged()
			return struct{}{}, nil
		}
		id := &st.Identities[i]
		pending, ok := id.Status.(statusPending)
		if !ok || now.Before(id.deadline(pending.Since)) {
			tx.Unchanged()
			return struct{}{}, nil
		}
		id.Status = statusFailed{}
		tx.Record(scope, journal.StateChange{
			Resource: identityARN(scope, name),
			From:     journal.ResourceState(sesapi.VerificationStatusPending), To: journal.ResourceState(sesapi.VerificationStatusFailed),
		})
		return struct{}{}, nil
	})
	if err != nil {
		s.log.LogAttrs(context.Background(), slog.LevelError, "an ses verification expiry failed; it runs again when a snapshot load re-arms the timer",
			slog.String("identity", key.ID), slog.String("error", err.Message))
	}
}

// Rearm arms one expiry timer per Pending identity at its deadline, since
// timers are never saved: an identity Pending at save time turns Failed at
// its original deadline (contract 1.2). It arms under the cell lock, where
// kernel.Write arms, so a concurrent write either commits first and is read
// here, or commits after and its own arms replace these.
func (s *Service) Rearm(clock.Instant) {
	sched := s.timers.Scheduler()
	for _, scope := range s.Scopes() {
		cell, ok := s.cells.Lookup(scope)
		if !ok {
			continue
		}
		store.Read(cell, func(st *state) struct{} {
			for i := range st.Identities {
				id := &st.Identities[i]
				if pending, ok := id.Status.(statusPending); ok {
					sched.Arm(s.expiry.Key(expiryTimerID(scope, id.Name)), id.deadline(pending.Since))
				}
			}
			return struct{}{}
		})
	}
}

// bindLink binds the raw operation behind the verification link.
func (s *Service) bindLink(b sesapi.Binder) {
	b.VerificationLink(s.verificationLink, registry.Full{})
}

// The verification link's answers (contract 8): understudy's own text, the
// owner's click having no AWS response to mirror. The first two are fmt
// formats whose %s is the identity.
const (
	linkVerifiedText = "understudy verified the SES identity %s.\n"
	linkExpiredText  = "The verification link for %s expired. Call VerifyEmailIdentity again for a new verification email.\n"
	linkUnknownText  = "This verification link names no SES identity: it was never issued, or its identity was deleted.\n"
	linkMethodText   = "The verification link answers GET only.\n"
)

// verificationLink serves GET VerificationLinkPath?token=<token>, the
// owner's click on the verification email's link (ses.md 11.3 item 1): a
// Pending identity becomes Success, with a StateChange and its expiry timer
// cancelled; a verified one stays so; an expired one is gone (410). Every
// answer is understudy-written text/plain, sandboxed and not sniffed, since
// it echoes an identity a client chose (ADR 0020). No IAM check: the link
// carries no credentials, as AWS's does not.
func (s *Service) verificationLink(_ context.Context, c *kernel.Call, r *http.Request, w http.ResponseWriter) *awserr.Error {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeLink(w, http.StatusMethodNotAllowed, linkMethodText)
		return nil
	}
	token := r.URL.Query().Get("token")
	scope, ok := s.tokenScope(token)
	if token == "" || !ok {
		writeLink(w, http.StatusNotFound, linkUnknownText)
		return nil
	}
	type outcome struct {
		status int
		name   string
	}
	got, err := kernel.Write(c, s.cells, scope, func(st *state, now clock.Instant, tx *kernel.Tx) (outcome, *awserr.Error) {
		i := st.findToken(token)
		if i < 0 {
			tx.Unchanged()
			return outcome{status: http.StatusNotFound}, nil
		}
		id := &st.Identities[i]
		if pending, ok := id.Status.(statusPending); ok && now.Before(id.deadline(pending.Since)) {
			id.Status = statusSuccess{}
			tx.Disarm(s.expiry, expiryTimerID(scope, id.Name))
			tx.Record(scope, journal.StateChange{
				Resource: identityARN(scope, id.Name),
				From:     journal.ResourceState(sesapi.VerificationStatusPending), To: journal.ResourceState(sesapi.VerificationStatusSuccess),
			})
			return outcome{status: http.StatusOK, name: id.Name}, nil
		}
		tx.Unchanged()
		if _, ok := id.Status.(statusSuccess); ok {
			return outcome{status: http.StatusOK, name: id.Name}, nil
		}
		return outcome{status: http.StatusGone, name: id.Name}, nil
	})
	if err != nil {
		return err
	}
	switch got.status {
	case http.StatusOK:
		c.Touch(identityARN(scope, got.name))
		writeLink(w, got.status, fmt.Sprintf(linkVerifiedText, got.name))
	case http.StatusGone:
		c.Touch(identityARN(scope, got.name))
		writeLink(w, got.status, fmt.Sprintf(linkExpiredText, got.name))
	default:
		writeLink(w, http.StatusNotFound, linkUnknownText)
	}
	return nil
}

// tokenScope is the scope holding the identity whose link token is token.
func (s *Service) tokenScope(token string) (aws.Scope, bool) {
	for _, scope := range s.Scopes() {
		cell, ok := s.cells.Lookup(scope)
		if ok && store.Read(cell, func(st *state) bool { return st.findToken(token) >= 0 }) {
			return scope, true
		}
	}
	return aws.Scope{}, false
}

// writeLink writes a verification link answer.
func writeLink(w http.ResponseWriter, status int, body string) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body)) //nolint:errcheck // the client went away; nothing is left to tell it
}
