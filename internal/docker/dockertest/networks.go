package dockertest

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"net/http"
	"net/netip"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/docker"
)

type network struct {
	id       string
	name     string
	internal bool
	labels   map[string]string
	subnet   netip.Prefix
	gateway  netip.Addr
	last     netip.Addr // the last address handed out
}

// addNetwork creates a bridge network on subnet; its first host address is
// the gateway. Callers hold d.mu.
func (d *Daemon) addNetwork(name string, subnet netip.Prefix, labels map[string]string, internal bool) *network {
	sum := sha256.Sum256([]byte("network/" + name))
	gateway := subnet.Masked().Addr().Next()
	n := &network{
		id:       hex.EncodeToString(sum[:]),
		name:     name,
		internal: internal,
		labels:   labels,
		subnet:   subnet,
		gateway:  gateway,
		last:     gateway,
	}
	d.networks[n.id] = n
	return n
}

// allocate hands out the next free address. Callers hold d.mu.
func (n *network) allocate() netip.Addr {
	n.last = n.last.Next()
	return n.last
}

// lookupNetwork finds a network by full ID, name or unique ID prefix, in that
// order. Callers hold d.mu.
func (d *Daemon) lookupNetwork(ref string) (*network, bool) {
	return resolve(d.networks, ref, func(n *network) string { return n.name })
}

// view renders a network the way inspect and list report it. Like moby, it
// lists only running containers: endpoints are created on start and
// released on stop, so a created or exited container's network config
// holds no endpoint. Callers hold d.mu.
func (d *Daemon) view(n *network) docker.Network {
	attached := map[string]docker.NetworkContainer{}
	for _, c := range d.containers {
		if c.status != statusRunning {
			continue
		}
		if ep, ok := c.networks[n.name]; ok {
			addr := ""
			if ep.IPAddress != "" {
				addr = ep.IPAddress + "/" + strconv.Itoa(n.subnet.Bits())
			}
			attached[c.id] = docker.NetworkContainer{Name: c.name, IPv4Address: addr}
		}
	}
	return docker.Network{
		ID:         n.id,
		Name:       n.name,
		Driver:     "bridge",
		Internal:   n.internal,
		IPAM:       docker.IPAM{Config: []docker.IPAMConfig{{Subnet: n.subnet.String(), Gateway: n.gateway.String()}}},
		Containers: attached,
		Labels:     maps.Clone(n.labels),
	}
}

func (d *Daemon) networkInspect(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n, ok := d.lookupNetwork(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "network "+r.PathValue("id")+" not found")
		return
	}
	writeJSON(w, http.StatusOK, d.view(n))
}

func (d *Daemon) networkList(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []docker.Network{}
	for _, n := range sortedNetworks(d.networks) {
		if labelsMatch(f["label"], n.labels) {
			v := d.view(n)
			v.Containers = map[string]docker.NetworkContainer{}
			out = append(out, v)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (d *Daemon) networkCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string            `json:"Name"`
		Internal bool              `json:"Internal"`
		Labels   map[string]string `json:"Labels"`
	}
	if err := decodeBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Name == "" {
		writeError(w, http.StatusBadRequest, "network name must not be empty")
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, n := range d.networks {
		if n.name == in.Name {
			writeError(w, http.StatusConflict, "network with name "+in.Name+" already exists")
			return
		}
	}
	d.subnets++
	subnet := netip.PrefixFrom(netip.AddrFrom4([4]byte{172, byte(17 + d.subnets), 0, 0}), 16) //nolint:gosec // tests create far fewer than 200 networks
	n := d.addNetwork(in.Name, subnet, in.Labels, in.Internal)
	d.emit("network", "create", n.id, map[string]string{"name": n.name, "type": "bridge"})
	writeJSON(w, http.StatusCreated, map[string]string{"Id": n.id, "Warning": ""})
}

func (d *Daemon) networkConnect(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Container      string                  `json:"Container"`
		EndpointConfig docker.EndpointSettings `json:"EndpointConfig"`
	}
	if err := decodeBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	n, ok := d.lookupNetwork(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "network "+r.PathValue("id")+" not found")
		return
	}
	c, ok := d.lookupContainer(in.Container)
	if !ok {
		writeError(w, http.StatusNotFound, "No such container: "+in.Container)
		return
	}
	if _, dup := c.networks[n.name]; dup {
		writeError(w, http.StatusForbidden, "endpoint with name "+c.name+" already exists in network "+n.name)
		return
	}
	c.networks[n.name] = in.EndpointConfig
	if c.status == statusRunning {
		c.assignAddresses(d)
	}
	d.emit("network", "connect", n.id, map[string]string{"name": n.name, "type": "bridge", "container": c.id})
	w.WriteHeader(http.StatusOK)
}

func (d *Daemon) networkRemove(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n, ok := d.lookupNetwork(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "network "+r.PathValue("id")+" not found")
		return
	}
	if n.name == d.opts.DefaultNetwork {
		writeError(w, http.StatusForbidden, n.name+" is a pre-defined network and cannot be removed")
		return
	}
	if len(d.view(n).Containers) > 0 {
		writeError(w, http.StatusForbidden, "error while removing network: network "+n.name+" id "+n.id+" has active endpoints")
		return
	}
	delete(d.networks, n.id)
	d.emit("network", "destroy", n.id, map[string]string{"name": n.name, "type": "bridge"})
	w.WriteHeader(http.StatusNoContent)
}
