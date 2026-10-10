package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/sustinbebustin/understudy/internal/codegen"
	"github.com/sustinbebustin/understudy/internal/ref"
)

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// errUsage reports a command line that names neither or both of -service
// and -all.
var errUsage = errors.New("understudy-gen: give exactly one of -service <id> or -all; see -h")

// run generates (or with -check, compares) the packages the flags name,
// finding the repository from the working directory.
func run(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("understudy-gen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	service := fs.String("service", "", "generate the service with this codegen/services/<id>.json")
	all := fs.Bool("all", false, "generate every service in codegen/services")
	check := fs.Bool("check", false, "compare the generated files with the inputs and fail on drift; write nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*service == "") == !*all || fs.NArg() > 0 {
		return errUsage
	}
	repo, err := codegen.FindRepo(".")
	if err != nil {
		return err
	}
	ids := []ref.ServiceID{ref.ServiceID(*service)}
	if *all {
		if ids, err = repo.Services(); err != nil {
			return err
		}
	}
	var drift []error
	for _, id := range ids {
		in, err := repo.Inputs(id)
		if err != nil {
			return err
		}
		files, err := codegen.Generate(in)
		if err != nil {
			return err
		}
		out := repo.OutputDir(in.Config)
		if !*check {
			if err := codegen.Write(out, files); err != nil {
				return err
			}
			continue
		}
		if err := codegen.Check(out, files); err != nil {
			drift = append(drift, err)
		}
	}
	return errors.Join(drift...)
}
