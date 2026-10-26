package authz_test

import (
	"errors"
	"testing"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ReadStored tells a missing policy, a readable one and an unreadable one
// apart (iam-enforcement-surfaces.md 12.10); ProvidedDocument folds the
// last back into none, as PolicyProvider.ResourcePolicy has always answered.
func TestReadStoredAndProvidedDocument(t *testing.T) {
	const readable = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"sqs:*","Resource":"*"}]}`
	const unreadable = `{"Statement":[{"Effect":"Deny","Principal":"*","Action":"sqs:*","Resource":"*","Bogus":1}]}`

	if got := authz.ReadStored(""); got != (authz.NoPolicy{}) {
		t.Errorf(`ReadStored("") = %#v, want NoPolicy`, got)
	}
	if got := authz.ProvidedDocument(authz.NoPolicy{}); got != nil {
		t.Errorf("ProvidedDocument(NoPolicy) = %+v, want nil", got)
	}

	p, ok := authz.ReadStored(readable).(authz.Parsed)
	want := policydoc.ParseStored(readable)
	if !ok || len(p.Document.Statements) != 1 || p.Document.Statements[0].Effect != want.Statements[0].Effect {
		t.Errorf("ReadStored(readable) = %#v, want Parsed with its one Deny", p)
	}
	if got := authz.ProvidedDocument(p); got == nil || len(got.Statements) != 1 {
		t.Errorf("ProvidedDocument(Parsed) = %+v, want its document", got)
	}

	u, ok := authz.ReadStored(unreadable).(authz.Unparsed)
	if _, isParseError := errors.AsType[*policydoc.ParseError](u.Err); !ok || u.Text != unreadable || !isParseError {
		t.Errorf("ReadStored(unreadable) = %#v, want Unparsed with the text and a *policydoc.ParseError", u)
	}
	if got := authz.ProvidedDocument(u); got != nil {
		t.Errorf("ProvidedDocument(Unparsed) = %+v, want nil", got)
	}
}
