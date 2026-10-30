package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/sustinbebustin/understudy/admin"
)

// SESMessages reads and deletes the messages SES recorded instead of
// sending them (ses.md 11.2). The SES service implements it through an
// adapter in internal/server; each method answers an *admin.Error for a
// query it refuses, naming the field.
type SESMessages interface {
	SESMessages(q admin.SESMessageQuery) (admin.SESMessagePage, *admin.Error)
	ClearSESMessages(q admin.SESMessageQuery) (admin.SESClearResult, *admin.Error)
}

// sesFilterParams are the parameters both SES message endpoints read.
func sesFilterParams() []string {
	return []string{admin.ParamSESAccount, admin.ParamSESRegion, admin.ParamSESMessageID, admin.ParamSESFrom, admin.ParamSESTo, admin.ParamSESSince}
}

func (h *Handler) sesMessages(w http.ResponseWriter, r *http.Request, _ []string) {
	if h.o.SES == nil {
		h.fail(w, r, notInBuild("reading SES messages"))
		return
	}
	v := r.URL.Query()
	q, err := sesQuery(v)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if s := v.Get(admin.ParamLimit); s != "" {
		n, perr := strconv.Atoi(s)
		if perr != nil || n <= 0 {
			h.fail(w, r, &admin.Error{
				Code: admin.InvalidRequest, Field: admin.ParamLimit,
				Message: fmt.Sprintf("%s=%q is not a page size", admin.ParamLimit, s),
				Hint:    "pass 1 to 1000, or leave " + admin.ParamLimit + " out for pages of 100",
			})
			return
		}
		q.Limit = n
	}
	q.Cursor = v.Get(admin.ParamCursor)
	page, err := h.o.SES.SESMessages(q)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	page.Messages = nonNil(page.Messages)
	h.ok(w, r, page)
}

func (h *Handler) sesMessagesClear(w http.ResponseWriter, r *http.Request, _ []string) {
	if h.o.SES == nil {
		h.fail(w, r, notInBuild("deleting SES messages"))
		return
	}
	q, err := sesQuery(r.URL.Query())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	res, err := h.o.SES.ClearSESMessages(q)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.ok(w, r, res)
}

// sesQuery reads the filters both SES message endpoints take. The hook
// parses account and region against the configured region table.
func sesQuery(v url.Values) (admin.SESMessageQuery, *admin.Error) {
	q := admin.SESMessageQuery{
		Account:   v.Get(admin.ParamSESAccount),
		Region:    v.Get(admin.ParamSESRegion),
		MessageID: v.Get(admin.ParamSESMessageID),
		From:      v.Get(admin.ParamSESFrom),
		To:        v.Get(admin.ParamSESTo),
	}
	if s := v.Get(admin.ParamSESSince); s != "" {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return q, &admin.Error{
				Code: admin.InvalidRequest, Field: admin.ParamSESSince,
				Message: fmt.Sprintf("%s=%q is not an RFC 3339 time", admin.ParamSESSince, s),
				Hint:    "pass emulator time such as 2026-01-01T00:00:00Z; `understudy clock` shows it",
			}
		}
		q.Since = t
	}
	return q, nil
}
