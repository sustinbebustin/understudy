package kernel

import (
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// ZeroTimestampError is an output whose required timestamp members were
// left at the zero time, which clients would read as year 1 (the Terraform
// provider crashes on some; gap-G6.md finding 1).
type ZeroTimestampError struct {
	Op      string
	Members []string
}

func (e *ZeroTimestampError) Error() string {
	return fmt.Sprintf("the %s output leaves required timestamps at the zero time: %s; set them from the commit instant",
		e.Op, strings.Join(e.Members, ", "))
}

// checkStrict is the Strict hook of the encode step: it scans out and
// refuses every required timestamp member holding the zero time.
func checkStrict(op *registry.Operation, out any) *awserr.Error {
	zero := scanOutput(out).zeroTimes
	if len(zero) == 0 {
		return nil
	}
	return awserr.Internalf(&ZeroTimestampError{Op: op.ID.String(), Members: zero})
}
