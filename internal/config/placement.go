package config

import (
	"errors"
	"io/fs"
	"strings"

	"github.com/sustinbebustin/understudy/internal/containermarker"
)

// Placement is where the process runs, which decides the default listener
// and whether ContainerPublished is allowed: only a container in its own
// network namespace, outside Kubernetes, is reached through published ports
// alone.
//
//sumtype:decl
type Placement interface{ isPlacement() }

// OnHost is a process outside any container.
type OnHost struct{}

// InContainer is a process inside a Docker or Podman container with its own
// network namespace, so only the ports published to the host (and the
// containers on its networks) reach it.
type InContainer struct{}

// HostNetworkContainer is a process inside a container that shares the
// host's network namespace (--network host), or whose own namespace
// /proc/sys/net does not show: 0.0.0.0 there is the host's every interface,
// which no published port guards.
type HostNetworkContainer struct{}

// InPod is a process in a Kubernetes pod, whose address every pod in the
// cluster reaches without a published port.
type InPod struct{}

func (OnHost) isPlacement()               {}
func (InContainer) isPlacement()          {}
func (HostNetworkContainer) isPlacement() {}
func (InPod) isPlacement()                {}

// KubernetesEnv is the variable the kubelet sets in every pod's containers;
// Load reads it as InPod.
const KubernetesEnv = "KUBERNETES_SERVICE_HOST"

// The network sysctls that tell the host's network namespace from a
// container's own: somaxconn is registered in every namespace,
// netdev_max_backlog only in the initial one.
const (
	perNamespaceSysctl = "/proc/sys/net/core/somaxconn"
	initialOnlySysctl  = "/proc/sys/net/core/netdev_max_backlog"
)

// DetectPlacement reads the container markers internal/containermarker
// reports (containers.md 8.3), where a kubepods cgroup means a Kubernetes pod.
// A container is InContainer only when /proc/sys/net shows a namespace of
// its own; one that cannot be confirmed counts as HostNetworkContainer.
// KUBERNETES_SERVICE_HOST set in env ("NAME=value" pairs) is a pod too.
// Load, doctor and the server's own exposure check all decide by it.
func DetectPlacement(readFile func(path string) ([]byte, error), env []string) Placement {
	for _, kv := range env {
		if name, text, _ := strings.Cut(kv, "="); name == KubernetesEnv && text != "" {
			return InPod{}
		}
	}
	found := containermarker.Read(readFile)
	if found.Names("kubepods") {
		return InPod{}
	}
	switch {
	case !found.Any():
		return OnHost{}
	case ownNetworkNamespace(readFile):
		return InContainer{}
	default:
		return HostNetworkContainer{}
	}
}

// ownNetworkNamespace reports whether /proc/sys/net shows a network
// namespace other than the host's: the per-namespace sysctl is there and
// the initial-namespace one is not. Anything else, such as an unreadable
// /proc, is not proof.
func ownNetworkNamespace(readFile func(path string) ([]byte, error)) bool {
	if _, err := readFile(perNamespaceSysctl); err != nil {
		return false
	}
	_, err := readFile(initialOnlySysctl)
	return errors.Is(err, fs.ErrNotExist)
}

// unpublished says why no published container port guards a listener in
// placement p; it is empty when one does.
func unpublished(p Placement) string {
	switch p.(type) {
	case InContainer:
		return ""
	case HostNetworkContainer:
		return "understudy runs in a container that shares the host's network namespace, or one " + perNamespaceSysctl + " and " +
			initialOnlySysctl + " do not show to be its own, so 0.0.0.0 is reachable without any published port"
	case InPod:
		return "understudy runs in a Kubernetes pod (" + KubernetesEnv + " or a kubepods cgroup), whose address every pod in the cluster reaches without a published port"
	case OnHost:
	}
	return "understudy is not running in a container (no /.dockerenv, /run/.containerenv or container cgroup)"
}
