package config

import (
	"net"
	"net/url"
	"path/filepath"
	"strings"
)

// dockerEnv is what the environment says about Docker, in the Docker CLI's
// variables (containers.md 2.1).
type dockerEnv struct {
	// host is DOCKER_HOST.
	host string
	// tlsVerify is DOCKER_TLS_VERIFY; any non-empty value, "0" included,
	// turns TLS on, as in the Docker CLI.
	tlsVerify string
	// certPath is DOCKER_CERT_PATH; empty means configDir.
	certPath string
	// configDir is DOCKER_CONFIG; empty means $HOME/.docker.
	configDir string
	home      string
}

// read takes name=text when it is one of the variables dockerEnv holds.
func (e *dockerEnv) read(name, text string) bool {
	switch name {
	case DockerHostEnv:
		e.host = text
	case DockerTLSVerifyEnv:
		e.tlsVerify = text
	case DockerCertPathEnv:
		e.certPath = text
	case DockerConfigEnv:
		e.configDir = text
	case "HOME":
		e.home = text
	default:
		return false
	}
	return true
}

// certDir is where the Docker CLI reads ca.pem, cert.pem and key.pem from:
// DOCKER_CERT_PATH, else DOCKER_CONFIG, else ~/.docker. Empty when none of
// them is set.
func (e *dockerEnv) certDir() string {
	switch {
	case e.certPath != "":
		return e.certPath
	case e.configDir != "":
		return e.configDir
	case e.home != "":
		return filepath.Join(e.home, ".docker")
	}
	return ""
}

// defaultDockerPort is the Docker CLI's port for a tcp:// host without one,
// with or without TLS (docker/cli opts/hosts.go ParseTCPAddr).
const defaultDockerPort = "2375"

// parseDocker reads disabled, a unix:// socket URL or a tcp:// address,
// which env makes TLS or plain; the reason is empty when it is valid and
// otherwise completes a sentence starting with the value.
func parseDocker(text string, env dockerEnv) (docker Docker, reason string) {
	if text == "disabled" {
		return DockerDisabled{}, ""
	}
	if addr, ok := strings.CutPrefix(text, "tcp://"); ok {
		return parseDockerTCP(addr, env)
	}
	path, ok := strings.CutPrefix(text, "unix://")
	if !ok {
		return nil, "is not disabled, a unix:// socket or a tcp:// address, the kinds understudy speaks to"
	}
	if !strings.HasPrefix(path, "/") {
		return nil, "does not name an absolute socket path"
	}
	return DockerSocket{Path: path}, ""
}

// parseDockerTCP reads host[:port] as the Docker CLI does: no port is 2375
// and no host is localhost. A base path, which the CLI keeps, is refused:
// understudy sends the Engine API at the root.
func parseDockerTCP(addr string, env dockerEnv) (docker Docker, reason string) {
	u, err := url.Parse("tcp://" + addr)
	switch {
	case err != nil:
		return nil, "is not a tcp://<host>:<port> address: " + err.Error()
	case u.User != nil:
		return nil, "carries user information, which a tcp:// daemon address cannot use"
	case u.Path != "" && u.Path != "/", u.RawQuery != "", u.Fragment != "":
		return nil, "has a path, query or fragment; understudy sends the Engine API at the daemon's root, so write tcp://<host>:<port>"
	}
	host, port := u.Hostname(), u.Port()
	if host == "" {
		host = "localhost"
	}
	if port == "" {
		port = defaultDockerPort
	}
	hostPort := net.JoinHostPort(host, port)
	if env.tlsVerify == "" {
		return DockerTCP{Addr: hostPort}, ""
	}
	dir := env.certDir()
	if dir == "" {
		return nil, "is a tcp:// address and " + DockerTLSVerifyEnv + " is set, but none of " + DockerCertPathEnv + ", " + DockerConfigEnv + " and HOME says where ca.pem, cert.pem and key.pem are; set " + DockerCertPathEnv
	}
	return DockerTLS{Addr: hostPort, CertDir: dir}, ""
}
