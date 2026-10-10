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

// errUsage reports a command line that names no target or more than one.
var errUsage = errors.New("understudy-gen: give exactly one of -service <id>, -all or -corpus; see -h")

// run generates (or with -check, compares) the packages the flags name,
// finding the repository from the working directory.
func run(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("understudy-gen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	service := fs.String("service", "", "generate the service with this codegen/services/<id>.json")
	all := fs.Bool("all", false, "generate every service in codegen/services")
	corpus := fs.Bool("corpus", false, "generate the synthetic protocol compliance services into internal/protocoltest/gen")
	check := fs.Bool("check", false, "compare the generated files with the inputs and fail on drift; write nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	targets := 0
	for _, t := range []bool{*service != "", *all, *corpus} {
		if t {
			targets++
		}
	}
	if targets != 1 || fs.NArg() > 0 {
		return errUsage
	}
	repo, err := codegen.FindRepo(".")
	if err != nil {
		return err
	}
	inputs, err := selectInputs(repo, *service, *all)
	if err != nil {
		return err
	}
	var drift []error
	for i := range inputs {
		files, err := codegen.Generate(inputs[i])
		if err != nil {
			return err
		}
		out := repo.OutputDir(inputs[i].Config)
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

// selectInputs reads the inputs of the named service, of every
// codegen/services file, or, when neither is named, of every corpus
// service.
func selectInputs(repo codegen.Repo, service string, all bool) ([]codegen.Inputs, error) {
	var out []codegen.Inputs
	switch {
	case service != "" || all:
		ids := []ref.ServiceID{ref.ServiceID(service)}
		if all {
			var err error
			if ids, err = repo.Services(); err != nil {
				return nil, err
			}
		}
		for _, id := range ids {
			in, err := repo.Inputs(id)
			if err != nil {
				return nil, err
			}
			out = append(out, in)
		}
	default:
		services := codegen.CorpusServices()
		for i := range services {
			in, err := repo.CorpusInputs(services[i])
			if err != nil {
				return nil, err
			}
			out = append(out, in)
		}
	}
	return out, nil
}
