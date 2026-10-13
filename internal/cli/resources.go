package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/admin"
)

func resourcesCmd() *command {
	return leaf("resources", "List every resource by service, account and region", "resources [--service S] [--account A] [--region R]",
		[]string{"understudy resources", "understudy resources --service sqs --region us-east-1", "understudy resources --output json"},
		func(fs *flag.FlagSet) action {
			var q admin.ResourceQuery
			fs.Var((*stringList)(&q.Services), "service", "service to list, such as sqs; repeat for more (default all)")
			fs.Var((*stringList)(&q.Accounts), "account", "account to list; repeat for more (default all)")
			fs.Var((*stringList)(&q.Regions), "region", "region to list; repeat for more (default all)")
			return func(ctx context.Context, r *runner, args []string) error {
				if err := wantArgs(args, 0, "no arguments"); err != nil {
					return err
				}
				c, err := r.client()
				if err != nil {
					return err
				}
				res, err := c.Resources(ctx, q)
				if err != nil {
					return err
				}
				return r.emit(admin.ResourceList{Resources: res}, func(w io.Writer) error {
					if len(res) == 0 {
						_, err := fmt.Fprintln(w, "no resources")
						return err
					}
					tw := newTable(w)
					fmt.Fprintln(tw, "SERVICE\tACCOUNT\tREGION\tARN\tSUMMARY")
					for _, x := range res {
						fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", x.Service, x.Account, x.Region, x.ARN, x.Summary)
					}
					return tw.Flush()
				})
			}
		})
}

func iamCmd() *command {
	return group("iam", "Show the IAM actions principals used, and the least-privilege policy they need",
		leaf("usage", "List the recorded uses of IAM actions", "iam usage [--principal ARN] [--service S]",
			[]string{"understudy iam usage", "understudy iam usage --principal arn:aws:iam::000000000000:role/worker --output json"},
			iamUsage),
		leaf("policy", "Print the least-privilege identity policy for a principal", "iam policy --principal ARN",
			[]string{"understudy iam policy --principal arn:aws:iam::000000000000:role/worker", "understudy iam policy --principal arn:aws:iam::000000000000:role/worker --output json"},
			iamPolicy),
	)
}

func iamUsage(fs *flag.FlagSet) action {
	var q admin.UsageQuery
	fs.StringVar(&q.Principal, "principal", "", "uses by this principal ARN or service principal")
	fs.StringVar(&q.Service, "service", "", "uses of this IAM action prefix, such as sqs")
	return func(ctx context.Context, r *runner, args []string) error {
		if err := wantArgs(args, 0, "no arguments"); err != nil {
			return err
		}
		c, err := r.client()
		if err != nil {
			return err
		}
		uses, err := c.IAMUsage(ctx, q)
		if err != nil {
			return err
		}
		return r.emit(admin.UsageList{Usage: uses}, func(w io.Writer) error { return writeUsage(w, uses) })
	}
}

func iamPolicy(fs *flag.FlagSet) action {
	principal := fs.String("principal", "", "the principal's IAM ARN (required)")
	return func(ctx context.Context, r *runner, args []string) error {
		if err := wantArgs(args, 0, "no arguments"); err != nil {
			return err
		}
		if *principal == "" {
			return &usageError{msg: "needs --principal ARN; 'understudy iam usage' lists the principals recorded"}
		}
		c, err := r.client()
		if err != nil {
			return err
		}
		p, err := c.IAMPolicy(ctx, *principal)
		if err != nil {
			return err
		}
		return r.emit(p, func(w io.Writer) error { return writePolicy(w, p) })
	}
}

func writeUsage(w io.Writer, uses []admin.Usage) error {
	if len(uses) == 0 {
		_, err := fmt.Fprintln(w, "no recorded uses")
		return err
	}
	tw := newTable(w)
	fmt.Fprintln(tw, "PRINCIPAL\tMODE\tACTION\tRESOURCE\tCALLS\tFAILURES\tLAST")
	for i := range uses {
		u := &uses[i]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%s\n", u.Principal, u.Mode, u.Action, u.Resource, u.Calls, u.Failures, u.Last.UTC().Format(time.RFC3339))
	}
	return tw.Flush()
}

func writePolicy(w io.Writer, p *admin.Policy) error {
	var b strings.Builder
	b.WriteString(string(p.Document))
	b.WriteString("\n")
	if len(p.ResourcePolicies) > 0 {
		b.WriteString("\nResource policies the recorded deliveries need:\n")
		for _, rp := range p.ResourcePolicies {
			fmt.Fprintf(&b, "  %s must allow %s to call %s from %s\n", rp.Target, rp.Principal, rp.Action, rp.Source)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func lambdaCmd() *command {
	return group("lambda", "Lambda conveniences",
		leaf("hot-reload", "Run a function's code from a host directory, reloaded on every change", "lambda hot-reload FUNCTION DIR [--account A] [--region R]",
			[]string{"understudy lambda hot-reload worker ./build/worker", "understudy lambda hot-reload worker /src/worker --region eu-west-1"},
			func(fs *flag.FlagSet) action {
				var req admin.HotReloadRequest
				fs.StringVar(&req.Account, "account", "", "the function's account (default the server's default account)")
				fs.StringVar(&req.Region, "region", "", "the function's region (default us-east-1)")
				return func(ctx context.Context, r *runner, args []string) error {
					if err := wantArgs(args, 2, "two arguments, the function and the directory"); err != nil {
						return err
					}
					dir, err := filepath.Abs(args[1])
					if err != nil {
						return fmt.Errorf("resolving %s: %w", args[1], err)
					}
					req.Dir = dir
					return r.simple(fmt.Sprintf("function %s now runs the code in %s", args[0], dir), func(c *admin.Client) error {
						return c.HotReload(ctx, args[0], req)
					})
				}
			}),
	)
}

func imagesCmd() *command {
	return group("images", "Container images understudy runs",
		leaf("pull", "Pull the pinned Lambda runtime images now, as a CI warm-up", "images pull [--runtime R]",
			[]string{"understudy images pull", "understudy images pull --runtime python3.13 --runtime provided.al2023"},
			func(fs *flag.FlagSet) action {
				var req admin.ImagePullRequest
				fs.Var((*stringList)(&req.Runtimes), "runtime", "runtime whose image to pull, such as python3.13; repeat for more (default all)")
				return func(ctx context.Context, r *runner, args []string) error {
					if err := wantArgs(args, 0, "no arguments"); err != nil {
						return err
					}
					c, err := r.slowClient()
					if err != nil {
						return err
					}
					res, err := c.PullImages(ctx, req)
					if err != nil {
						return err
					}
					return r.emit(res, func(w io.Writer) error {
						_, err := fmt.Fprintf(w, "pulled %s\n", orNone(res.Images))
						return err
					})
				}
			}),
	)
}
