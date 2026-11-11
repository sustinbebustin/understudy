package sqs

import (
	"encoding/json/jsontext"
	"encoding/json/v2"

	"github.com/sustinbebustin/understudy/internal/ids"
)

// receiptIssuers maps every receipt handle a message of the state still
// holds to the name of the queue that issued it. It tells a handle of
// another queue, which a batch entry fails as not valid for this queue
// (sqs/R01 06-delete-batch, 06-cmv-batch), from one that reaches no
// message, without scanning every queue: handles carry no queue, so the
// queue is found by lookup. It is derived from the messages, rebuilt when
// a state is read and kept in step by every write that issues or drops a
// handle: a receive (State.recordIssue) and each removal of a message
// (State.forget). The state is changed in place under its cell's lock, so
// the map is too.
type receiptIssuers map[ids.ReceiptHandle]string

// recordIssue records that queue issued handle.
func (st *State) recordIssue(handle ids.ReceiptHandle, queue string) {
	if st.issuers == nil {
		st.issuers = receiptIssuers{}
	}
	st.issuers[handle] = queue
}

// forget drops the handles of m, which leaves its queue.
func (st *State) forget(m *Message) {
	for h := range m.handles() {
		delete(st.issuers, h)
	}
}

// issuedElsewhere reports whether a queue other than queue issued handle
// to a message the state still holds.
func (st *State) issuedElsewhere(handle, queue string) bool {
	by, ok := st.issuers[ids.ReceiptHandle(handle)]
	return ok && by != queue
}

// reindex rebuilds issuers from the messages.
func (st *State) reindex() {
	st.issuers = receiptIssuers{}
	for name := range st.Queues {
		queue := st.Queues[name]
		for _, m := range queue.Messages.all() {
			for h := range m.handles() {
				st.issuers[h] = name
			}
		}
	}
}

// stateFields is State without its methods, so UnmarshalJSONFrom decodes
// the fields without calling itself.
type stateFields State

// UnmarshalJSONFrom reads a State as its fields marshal, then derives its
// receipt handle issuers.
func (st *State) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	if err := json.UnmarshalDecode(dec, (*stateFields)(st)); err != nil {
		return err
	}
	st.reindex()
	return nil
}
