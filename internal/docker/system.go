package docker

import (
	"context"
	"net/http"
	"slices"
)

// Info is the subset of GET /info that capability and topology checks read
// (containers.md 2.2, 10.2).
type Info struct {
	ID              string   `json:"ID"`
	Name            string   `json:"Name"`
	ServerVersion   string   `json:"ServerVersion"`
	OperatingSystem string   `json:"OperatingSystem"`
	OSType          string   `json:"OSType"`
	Architecture    string   `json:"Architecture"`
	NCPU            int      `json:"NCPU"`
	MemTotal        int64    `json:"MemTotal"`
	Driver          string   `json:"Driver"`
	LoggingDriver   string   `json:"LoggingDriver"`
	CgroupDriver    string   `json:"CgroupDriver"`
	CgroupVersion   string   `json:"CgroupVersion"`
	MemoryLimit     bool     `json:"MemoryLimit"`
	SwapLimit       bool     `json:"SwapLimit"`
	PidsLimit       bool     `json:"PidsLimit"`
	SecurityOptions []string `json:"SecurityOptions"`
}

// Rootless reports whether the daemon runs rootless: SecurityOptions
// contains "name=rootless" (Docker and Podman both report it).
func (i Info) Rootless() bool {
	return slices.Contains(i.SecurityOptions, "name=rootless")
}

// Info reads GET /info.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var info Info
	err := c.callJSON(ctx, "Info", http.MethodGet, "/info", nil, nil, &info)
	return info, err
}
