package admin

import (
	"net/http"

	"github.com/sustinbebustin/understudy/admin"
)

// coverage answers unavailable: the coverage report needs the protocol-test
// baseline, which this build does not carry yet.
func (h *Handler) coverage(w http.ResponseWriter, r *http.Request, _ []string) {
	h.fail(w, r, &admin.Error{
		Code:    admin.Unavailable,
		Message: "this build does not produce the coverage report yet",
		Hint:    "GET " + admin.Prefix + "/resources lists what the emulator holds; the report arrives with the protocol-test baseline",
	})
}
