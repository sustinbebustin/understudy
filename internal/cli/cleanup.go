package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/sustinbebustin/understudy/internal/config"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/server"
)

// swept is what cleanup removed.
type swept struct {
	// Owner is the understudy.dev/owner label swept; empty with --all.
	Owner      string   `json:"owner,omitempty"`
	Containers []string `json:"containers"`
	Networks   []string `json:"networks"`
}

func cleanupCmd() *command {
	return leaf("cleanup", "Remove the containers and networks a crashed understudy left on this Docker daemon",
		"cleanup [--config FILE] [--persist DIR] [--all]",
		[]string{
			"understudy cleanup --persist ./.understudy",
			"understudy cleanup --config understudy.json --output json",
			"understudy cleanup --all",
		},
		func(fs *flag.FlagSet) action {
			var f cleanupFlags
			fs.StringVar(&f.config, "config", "", "configuration whose Docker and persistence settings to use (default UNDERSTUDY_CONFIG, else ./understudy.json when it exists)")
			fs.StringVar(&f.persist, "persist", "", "the --persist directory of the deployment to clean up (default the configuration's); refused while a serve holds it")
			fs.BoolVar(&f.all, "all", false, "remove the containers of every understudy on this Docker daemon, including other running instances such as parallel CI jobs and understudytest servers")
			return func(ctx context.Context, r *runner, args []string) error {
				if err := wantArgs(args, 0, "no arguments"); err != nil {
					return err
				}
				return r.cleanup(ctx, f)
			}
		})
}

// cleanupFlags are the flags of cleanup.
type cleanupFlags struct {
	config, persist string
	all             bool
}

func (r *runner) cleanup(ctx context.Context, f cleanupFlags) error {
	var args []string
	if f.config != "" {
		args = append(args, "--"+config.ConfigFlag, f.config)
	}
	if f.persist != "" {
		args = append(args, "--persist", f.persist)
	}
	host, cerr := config.LoadHost(args, r.o.Env, r.o.ReadFile)
	if cerr != nil {
		return &usageError{msg: cerr.Error()}
	}
	var res swept
	var err error
	dir, persists := host.Persistence.(config.Dir)
	switch {
	case f.all:
		if h := r.healthNow(ctx); h != nil {
			return fmt.Errorf("an understudy is serving at %s, and cleanup --all would remove its containers; stop it first, since a running understudy removes its own containers on shutdown", r.endpointURL())
		}
		res, err = sweepAll(ctx, host.Docker)
	case persists:
		res, err = sweepDataDir(ctx, host.Docker, dir.Path)
		if locked, ok := errors.AsType[*server.DataDirLockedError](err); ok {
			return &localError{
				op:   "cleanup --persist " + locked.Path,
				err:  &dataDirHeldError{dir: locked.Path},
				next: "stop that serve, which removes its own containers on shutdown; cleanup is for the leftovers of one that crashed",
			}
		}
	default:
		return &usageError{msg: "the configuration persists no state, and a run without --persist labels its containers with an owner drawn at start, which cleanup cannot derive; " +
			"pass the crashed run's --persist directory, or --all to remove the containers of every understudy on this Docker daemon, including running ones"}
	}
	if emitErr := r.emit(res, func(w io.Writer) error {
		_, err := fmt.Fprintf(w, "removed %d container(s) and %d network(s)\n", len(res.Containers), len(res.Networks))
		return err
	}); emitErr != nil {
		return errors.Join(err, emitErr)
	}
	return err
}

// dataDirHeldError is a persistence directory whose lock a running serve
// holds.
type dataDirHeldError struct{ dir string }

func (e *dataDirHeldError) Error() string {
	return "an understudy serve is running with --persist " + e.dir + ", and cleanup would remove its containers"
}

// sweepDataDir removes what a crashed serve persisting under dir left,
// through server.SweepDataDir, which shares serve's owner label and data
// directory lock: a *server.DataDirLockedError while a serve holds it.
// Nothing is removed with Docker disabled.
func sweepDataDir(ctx context.Context, d config.Docker, dir string) (swept, error) {
	res := swept{Containers: []string{}, Networks: []string{}}
	c, ok, err := dockerClient(d)
	if !ok || err != nil {
		return res, err
	}
	defer c.Close()
	owner, s, err := server.SweepDataDir(ctx, c, dir)
	res.Owner = owner
	res.Containers = append(res.Containers, s.Containers...)
	res.Networks = append(res.Networks, s.Networks...)
	return res, err
}

// dockerClient is the client for d; ok is false with Docker disabled.
func dockerClient(d config.Docker) (c *docker.Client, ok bool, err error) {
	sock, ok := d.(config.DockerSocket)
	if !ok {
		return nil, false, nil
	}
	c, err = docker.New(docker.Options{Socket: sock.Path})
	return c, err == nil, err
}

// sweepAll removes the containers and empty networks labeled
// understudy.dev/managed of every owner, except those a test harness
// labeled understudy.dev/test, which their harness removes. Running
// instances lose their containers too, as --all says. Failures on single
// objects do not stop the sweep; they are joined.
func sweepAll(ctx context.Context, d config.Docker) (swept, error) {
	res := swept{Containers: []string{}, Networks: []string{}}
	c, ok, err := dockerClient(d)
	if !ok || err != nil {
		return res, err
	}
	defer c.Close()
	selector := map[string]string{docker.LabelManaged: "true"}
	containers, err := c.ContainerList(ctx, selector)
	if err != nil {
		return res, fmt.Errorf("listing understudy's containers: %w", err)
	}
	var errs []error
	for _, ct := range containers {
		if ct.Labels[docker.LabelTest] == "1" {
			continue
		}
		err := c.ContainerRemove(ctx, ct.ID, docker.RemoveOptions{Force: true, Volumes: true})
		switch {
		case err == nil:
			res.Containers = append(res.Containers, ct.ID)
		case !docker.IsNotFound(err):
			errs = append(errs, fmt.Errorf("removing container %s: %w", ct.ID, err))
		}
	}
	networks, err := c.NetworkList(ctx, selector)
	if err != nil {
		return res, errors.Join(append(errs, fmt.Errorf("listing understudy's networks: %w", err))...)
	}
	for _, n := range networks {
		if n.Labels[docker.LabelTest] == "1" {
			continue
		}
		removed, err := removeIfEmpty(ctx, c, n.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("removing network %s: %w", n.Name, err))
		}
		if removed {
			res.Networks = append(res.Networks, n.ID)
		}
	}
	return res, errors.Join(errs...)
}

func removeIfEmpty(ctx context.Context, c *docker.Client, id string) (bool, error) {
	n, err := c.NetworkInspect(ctx, id)
	switch {
	case docker.IsNotFound(err):
		return false, nil
	case err != nil:
		return false, err
	case len(n.Containers) > 0:
		return false, nil
	}
	err = c.NetworkRemove(ctx, id)
	if docker.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}
