package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
)

// tree is the understudy command tree.
func tree() *command {
	root := group("understudy", "understudy is a local AWS emulator for development machines and CI.",
		serveCmd(),
		healthCmd(),
		settleCmd(),
		resetCmd(),
		clockCmd(),
		faultsCmd(),
		journalCmd(),
		snapshotCmd(),
		resourcesCmd(),
		coverageCmd(),
		iamCmd(),
		lambdaCmd(),
		imagesCmd(),
		envCmd(),
		doctorCmd(),
		cleanupCmd(),
		versionCmd(),
	)
	root.usage = "<command> [flags]"
	root.examples = []string{
		"understudy serve",
		"understudy health --wait 30s",
		"understudy journal --follow --service sqs",
	}
	root.epilog = exitCodes
	return root
}

// exitCodes is the root help's list of exit codes, admin.ExitCode's.
const exitCodes = `Exit codes:
  0  success
  1  the operation failed
  2  usage error: fix the command line
  3  understudy is not reachable at the endpoint, or did not answer in time
  4  a condition was not met, such as health --wait or settle timing out
  5  the server refused the admin token (UNDERSTUDY_ADMIN_TOKEN)
`

func versionCmd() *command {
	return leaf("version", "Print the version", "version",
		[]string{"understudy version", "understudy version --output json"},
		func(*flag.FlagSet) action {
			return func(_ context.Context, r *runner, args []string) error {
				if err := wantArgs(args, 0, "no arguments"); err != nil {
					return err
				}
				return r.emit(struct {
					Version string `json:"version"`
				}{r.o.Version}, func(w io.Writer) error {
					_, err := fmt.Fprintf(w, "understudy %s\n", r.o.Version)
					return err
				})
			}
		})
}
