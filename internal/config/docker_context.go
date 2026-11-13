package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
)

// defaultContext is the Docker CLI's name for the context that is
// DOCKER_HOST, else the default socket.
const defaultContext = "default"

// contextName is the Docker context the Docker CLI picks when DOCKER_HOST
// is unset (containers.md 2.1): DOCKER_CONTEXT, else currentContext in
// config.json. It is empty or default for the default context, and setting
// spells what named it. A config.json that exists but cannot be read or
// parsed is a warning and the default context, as the Docker CLI warns and
// goes on.
func (e *dockerEnv) contextName(readFile func(string) ([]byte, error)) (name, setting string, warn *Warning) {
	if e.context != "" {
		return e.context, DockerContextEnv, nil
	}
	dir := e.cliConfigDir()
	if dir == "" {
		return "", "", nil
	}
	path := filepath.Join(dir, "config.json")
	data, err := readFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", "", nil
	}
	var cfg struct {
		CurrentContext string `json:"currentContext"`
	}
	if err == nil {
		err = json.Unmarshal(data, &cfg)
	}
	if err != nil {
		return "", "", &Warning{
			Setting: path,
			Message: "the Docker CLI configuration " + quote(path) + " cannot be read as JSON (" + err.Error() + "), so understudy uses the default Docker context, unix://" + DefaultDockerSocket,
			Fix:     "fix or remove the file, or name the Docker Engine with DOCKER_CONTEXT, DOCKER_HOST or UNDERSTUDY_DOCKER",
		}
	}
	return cfg.CurrentContext, path + ": currentContext", nil
}

// unusableContext says why understudy cannot use a Docker context: reason
// completes a sentence starting with the context, and fix is how to act on
// it.
type unusableContext struct{ reason, fix string }

// contextFix is the fix for a context understudy cannot use, unless a more
// specific one applies.
const contextFix = "set UNDERSTUDY_DOCKER to a unix:// socket or a tcp:// address to use one, or to disabled to silence this warning; DOCKER_CONTEXT or `docker context use` picks another context"

// contextDocker reads the Docker endpoint of the context name from
// <config dir>/contexts/meta/<lowercase hex SHA-256 of name>/meta.json
// (containers.md 2.1). Its TLS settings are the context's own, not
// DOCKER_TLS_VERIFY's: the Docker CLI speaks TLS to a tcp:// endpoint that
// sets SkipTLSVerify or keeps ca.pem, cert.pem or key.pem in
// <config dir>/contexts/tls/<digest>/docker (containers.md 2.1), which
// understudy does not read.
func (e *dockerEnv) contextDocker(name string, readFile func(string) ([]byte, error)) (Docker, *unusableContext) {
	dir := e.cliConfigDir()
	if dir == "" {
		return nil, &unusableContext{reason: "is named, but neither " + DockerConfigEnv + " nor HOME says where the Docker CLI keeps its contexts", fix: contextFix}
	}
	sum := sha256.Sum256([]byte(name))
	digest := hex.EncodeToString(sum[:])
	contexts := filepath.Join(dir, "contexts")
	metaPath := filepath.Join(contexts, "meta", digest, "meta.json")
	data, err := readFile(metaPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, &unusableContext{reason: "does not exist: there is no " + metaPath, fix: contextFix}
	case err != nil:
		return nil, &unusableContext{reason: "has a meta file understudy cannot read: " + err.Error(), fix: contextFix}
	}
	var meta struct {
		Endpoints struct {
			Docker *struct {
				Host          string
				SkipTLSVerify bool
			} `json:"docker"`
		}
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, &unusableContext{reason: "has a meta file, " + metaPath + ", that is not JSON: " + err.Error(), fix: contextFix}
	}
	ep := meta.Endpoints.Docker
	if ep == nil || ep.Host == "" {
		return nil, &unusableContext{reason: "has no Docker endpoint in " + metaPath, fix: contextFix}
	}
	docker, reason := parseDocker(ep.Host, dockerEnv{})
	if _, off := docker.(DockerDisabled); off {
		reason = "is not a unix:// socket or a tcp:// address"
	}
	if reason != "" {
		return nil, &unusableContext{reason: "has the endpoint " + quote(ep.Host) + ", which " + reason, fix: contextFix}
	}
	tcp, ok := docker.(DockerTCP)
	if !ok {
		return docker, nil
	}
	if ep.SkipTLSVerify {
		return nil, &unusableContext{reason: "speaks TLS to " + quote(ep.Host) + " without verifying the daemon, which understudy does not do", fix: contextFix}
	}
	certs := filepath.Join(contexts, "tls", digest, "docker")
	for _, file := range []string{"ca.pem", "cert.pem", "key.pem"} {
		if _, err := readFile(filepath.Join(certs, file)); err == nil {
			return nil, &unusableContext{
				reason: "speaks TLS to " + quote(ep.Host) + " with certificates in " + certs + ", which understudy reads only through " + DockerTLSVerifyEnv + " and " + DockerCertPathEnv,
				fix:    "set UNDERSTUDY_DOCKER=tcp://" + tcp.Addr + " with " + DockerTLSVerifyEnv + "=1 and " + DockerCertPathEnv + "=" + certs + ", or UNDERSTUDY_DOCKER=disabled to silence this warning",
			}
		}
	}
	return docker, nil
}
