package docker

import (
	"context"
	"errors"
	"fmt"
)

// Swept lists what a Sweep removed.
type Swept struct {
	Containers []string
	Networks   []string
}

// Sweep removes the leftovers of o.Owner's earlier instances: every managed
// container of that owner whose instance label differs from o.Instance
// (force, with volumes), then every such network left without containers
// (containers.md 9.3). Objects of other owners are never touched. The caller
// must hold the owner's data-directory lock, so that no live instance of the
// same owner is swept. Failures on single objects do not stop the sweep;
// they are joined into the returned error. An invalid Ownership is refused
// with an *OwnershipError before anything is listed.
func (c *Client) Sweep(ctx context.Context, o Ownership) (Swept, error) {
	var swept Swept
	if err := o.Validate(); err != nil {
		return swept, fmt.Errorf("sweep refused: %w", err)
	}
	var errs []error
	containers, err := c.ContainerList(ctx, ownerSelector(o.Owner))
	if err != nil {
		return swept, fmt.Errorf("sweep owner %q: list containers: %w", o.Owner, err)
	}
	for _, ct := range containers {
		if ct.Labels[LabelInstance] == o.Instance {
			continue
		}
		err := c.ContainerRemove(ctx, ct.ID, RemoveOptions{Force: true, Volumes: true})
		switch {
		case err == nil:
			swept.Containers = append(swept.Containers, ct.ID)
		case !IsNotFound(err):
			errs = append(errs, fmt.Errorf("sweep owner %q: remove container %s: %w", o.Owner, ct.ID, err))
		}
	}
	networks, err := c.NetworkList(ctx, ownerSelector(o.Owner))
	if err != nil {
		errs = append(errs, fmt.Errorf("sweep owner %q: list networks: %w", o.Owner, err))
		return swept, errors.Join(errs...)
	}
	for _, n := range networks {
		if n.Labels[LabelInstance] == o.Instance {
			continue
		}
		removed, err := c.removeIfEmpty(ctx, n.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("sweep owner %q: network %s: %w", o.Owner, n.Name, err))
		}
		if removed {
			swept.Networks = append(swept.Networks, n.ID)
		}
	}
	return swept, errors.Join(errs...)
}

// RemoveInstance removes every managed container of o's own instance
// (force, with volumes), then every such network left without containers:
// a shutdown's last defence for containers its orderly stop missed. It
// shares Sweep's error handling and refuses an invalid Ownership the same
// way.
func (c *Client) RemoveInstance(ctx context.Context, o Ownership) (Swept, error) {
	var swept Swept
	if err := o.Validate(); err != nil {
		return swept, fmt.Errorf("instance removal refused: %w", err)
	}
	selector := ownerSelector(o.Owner)
	selector[LabelInstance] = o.Instance
	var errs []error
	containers, err := c.ContainerList(ctx, selector)
	if err != nil {
		return swept, fmt.Errorf("remove instance %q: list containers: %w", o.Instance, err)
	}
	for _, ct := range containers {
		err := c.ContainerRemove(ctx, ct.ID, RemoveOptions{Force: true, Volumes: true})
		switch {
		case err == nil:
			swept.Containers = append(swept.Containers, ct.ID)
		case !IsNotFound(err):
			errs = append(errs, fmt.Errorf("remove instance %q: remove container %s: %w", o.Instance, ct.ID, err))
		}
	}
	networks, err := c.NetworkList(ctx, selector)
	if err != nil {
		errs = append(errs, fmt.Errorf("remove instance %q: list networks: %w", o.Instance, err))
		return swept, errors.Join(errs...)
	}
	for _, n := range networks {
		removed, err := c.removeIfEmpty(ctx, n.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("remove instance %q: network %s: %w", o.Instance, n.Name, err))
		}
		if removed {
			swept.Networks = append(swept.Networks, n.ID)
		}
	}
	return swept, errors.Join(errs...)
}

func (c *Client) removeIfEmpty(ctx context.Context, id string) (bool, error) {
	n, err := c.NetworkInspect(ctx, id)
	if err != nil {
		if IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if len(n.Containers) > 0 {
		return false, nil
	}
	err = c.NetworkRemove(ctx, id)
	if IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}
