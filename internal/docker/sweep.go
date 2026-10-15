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
	if err := o.Validate(); err != nil {
		return Swept{}, fmt.Errorf("sweep refused: %w", err)
	}
	ownInstance := func(labels map[string]string) bool { return labels[LabelInstance] == o.Instance }
	return c.RemoveManaged(ctx, ownerSelector(o.Owner), ownInstance, fmt.Sprintf("sweep owner %q", o.Owner))
}

// RemoveInstance removes every managed container of o's own instance
// (force, with volumes), then every such network left without containers:
// a shutdown's last defence for containers its orderly stop missed. It
// shares Sweep's error handling and refuses an invalid Ownership the same
// way.
func (c *Client) RemoveInstance(ctx context.Context, o Ownership) (Swept, error) {
	if err := o.Validate(); err != nil {
		return Swept{}, fmt.Errorf("instance removal refused: %w", err)
	}
	selector := ownerSelector(o.Owner)
	selector[LabelInstance] = o.Instance
	return c.RemoveManaged(ctx, selector, nil, fmt.Sprintf("remove instance %q", o.Instance))
}

// RemoveManaged removes every container matching selector (force, with
// volumes), then every network matching it that is left without
// containers. Objects whose labels keep reports true are left alone; a nil
// keep keeps none. operation names the removal for error text only
// ("sweep owner \"x\""). Failures on single objects do not stop the
// removal; they are joined into the returned error, each prefixed with
// operation. A failure to list the containers stops it before anything is
// removed.
func (c *Client) RemoveManaged(ctx context.Context, selector map[string]string, keep func(labels map[string]string) bool, operation string) (Swept, error) {
	var swept Swept
	kept := func(labels map[string]string) bool { return keep != nil && keep(labels) }
	containers, err := c.ContainerList(ctx, selector)
	if err != nil {
		return swept, fmt.Errorf("%s: list containers: %w", operation, err)
	}
	var errs []error
	for _, ct := range containers {
		if kept(ct.Labels) {
			continue
		}
		err := c.ContainerRemove(ctx, ct.ID, RemoveOptions{Force: true, Volumes: true})
		switch {
		case err == nil:
			swept.Containers = append(swept.Containers, ct.ID)
		case !IsNotFound(err):
			errs = append(errs, fmt.Errorf("%s: remove container %s: %w", operation, ct.ID, err))
		}
	}
	networks, err := c.NetworkList(ctx, selector)
	if err != nil {
		errs = append(errs, fmt.Errorf("%s: list networks: %w", operation, err))
		return swept, errors.Join(errs...)
	}
	for _, n := range networks {
		if kept(n.Labels) {
			continue
		}
		removed, err := c.removeIfEmpty(ctx, n.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: network %s: %w", operation, n.Name, err))
		}
		if removed {
			swept.Networks = append(swept.Networks, n.ID)
		}
	}
	return swept, errors.Join(errs...)
}

// removeIfEmpty removes network id unless containers are still attached to
// it, reporting whether it did. A network already gone is not an error.
func (c *Client) removeIfEmpty(ctx context.Context, id string) (bool, error) {
	n, err := c.NetworkInspect(ctx, id)
	switch {
	case IsNotFound(err):
		return false, nil
	case err != nil:
		return false, err
	case len(n.Containers) > 0:
		return false, nil
	}
	err = c.NetworkRemove(ctx, id)
	if IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}
