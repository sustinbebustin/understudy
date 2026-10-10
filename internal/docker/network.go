package docker

import (
	"context"
	"net/http"
)

// Network is the subset of GET /networks/{id} that topology detection and
// sweeping read.
type Network struct {
	ID         string                      `json:"Id"`
	Name       string                      `json:"Name"`
	Driver     string                      `json:"Driver"`
	Internal   bool                        `json:"Internal"`
	IPAM       IPAM                        `json:"IPAM"`
	Containers map[string]NetworkContainer `json:"Containers"`
	Labels     map[string]string           `json:"Labels"`
}

// IPAM is a network's address management configuration.
type IPAM struct {
	Config []IPAMConfig `json:"Config"`
}

// IPAMConfig is one address pool; Gateway is the host side of the bridge.
type IPAMConfig struct {
	Subnet  string `json:"Subnet"`
	Gateway string `json:"Gateway"`
}

// NetworkContainer is a container attached to a network, keyed by ID.
type NetworkContainer struct {
	Name        string `json:"Name"`
	IPv4Address string `json:"IPv4Address"`
}

// NetworkInspect reads a network by name or ID.
func (c *Client) NetworkInspect(ctx context.Context, id string) (Network, error) {
	var out Network
	err := c.callJSON(ctx, "NetworkInspect", http.MethodGet, "/networks/"+id, nil, nil, &out)
	return out, err
}

// NetworkOptions configures NetworkCreate.
type NetworkOptions struct {
	// Internal cuts the network off from outside connectivity.
	Internal bool
	// Labels mark the network for sweeping.
	Labels map[string]string
}

// NetworkCreate creates a bridge network and returns its ID. A duplicate
// name is a conflict (always, since API 1.44).
func (c *Client) NetworkCreate(ctx context.Context, name string, opts NetworkOptions) (string, error) {
	in := struct {
		Name     string            `json:"Name"`
		Driver   string            `json:"Driver"`
		Internal bool              `json:"Internal,omitempty"`
		Labels   map[string]string `json:"Labels,omitempty"`
	}{Name: name, Driver: "bridge", Internal: opts.Internal, Labels: opts.Labels}
	var out struct {
		ID string `json:"Id"`
	}
	err := c.callJSON(ctx, "NetworkCreate", http.MethodPost, "/networks/create", nil, in, &out)
	return out.ID, err
}

// NetworkConnect attaches a container to a network.
func (c *Client) NetworkConnect(ctx context.Context, network, container string, endpoint EndpointSettings) error {
	in := struct {
		Container      string           `json:"Container"`
		EndpointConfig EndpointSettings `json:"EndpointConfig"`
	}{Container: container, EndpointConfig: endpoint}
	return c.callJSON(ctx, "NetworkConnect", http.MethodPost, "/networks/"+network+"/connect", nil, in, nil)
}

// NetworkList lists the networks that carry all of labels. The daemon does
// not fill Containers in a listing; inspect a network to see them.
func (c *Client) NetworkList(ctx context.Context, labels map[string]string) ([]Network, error) {
	q, err := labelQuery(labels)
	if err != nil {
		return nil, err
	}
	var out []Network
	err = c.callJSON(ctx, "NetworkList", http.MethodGet, "/networks", q, nil, &out)
	return out, err
}

// NetworkRemove deletes a network. A network with attached containers is
// refused by the daemon.
func (c *Client) NetworkRemove(ctx context.Context, id string) error {
	return c.callJSON(ctx, "NetworkRemove", http.MethodDelete, "/networks/"+id, nil, nil, nil)
}
