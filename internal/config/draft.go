package config

import (
	"log/slog"
	"math"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// draft is a configuration being loaded: the defaults with each setting
// applied, plus the settings that only resolve together (the listener and
// its auth, TLS, persistence, the licence, Docker and the clock), kept with
// where they came from until finish.
type draft struct {
	placement  Placement
	cfg        Config
	dockerHost string

	listen      *netip.AddrPort
	listenAt    string
	token       *Secret
	tokenAt     string
	auth        *authChoice
	authAt      string
	awsAPI      bool
	awsAPIAt    string
	tls         *bool
	tlsAt       string
	tlsListen   *netip.AddrPort
	tlsListenAt string
	persist     string
	every       *time.Duration
	everyAt     string
	accept      bool
	acceptAt    string
	docker      Docker
	dynamoOff   bool
	dynamoAt    string
	// frozen is an explicit --clock frozen setting, which no scale may
	// join.
	frozen   *Frozen
	frozenAt string
	scale    *float64
	scaleAt  string
}

// authChoice is an explicit listenAuth.
type authChoice uint8

const (
	authToken authChoice = iota + 1
	authContainerPublished
)

func newDraft(p Placement, dockerHost string) *draft {
	return &draft{placement: p, cfg: Defaults(p), dockerHost: dockerHost}
}

// setters apply each scalar setting's text to a draft.
func setters() map[string]func(*draft, value) *Error {
	return map[string]func(*draft, value) *Error{
		keyListen:       (*draft).setListen,
		keyAdminToken:   (*draft).setToken,
		keyListenAuth:   (*draft).setAuth,
		keyAWSAPI:       (*draft).setAWSAPI,
		keyTLS:          (*draft).setTLS,
		keyTLSListen:    (*draft).setTLSListen,
		keyPersist:      (*draft).setPersist,
		keyPersistEvery: (*draft).setEvery,
		keyLoadSnapshot: func(d *draft, v value) *Error { return nonEmpty(v, &d.cfg.LoadSnapshot) },
		keySeed:         (*draft).setSeed,
		keyClock:        (*draft).setClock,
		keyClockScale:   (*draft).setScale,
		keySigV4:        (*draft).setSigV4,
		keyDocker:       (*draft).setDocker,
		keyAccept:       (*draft).setAccept,
		keyReadyFile:    func(d *draft, v value) *Error { return nonEmpty(v, &d.cfg.ReadyFile) },
		keyLogFormat:    (*draft).setLogFormat,
		keyLogLevel:     (*draft).setLogLevel,
	}
}

func bad(v value, reason, hint string) *Error {
	return &Error{Where: v.where, Reason: quote(v.text) + " " + reason, Hint: hint}
}

func nonEmpty(v value, dst *string) *Error {
	if v.text == "" {
		return &Error{Where: v.where, Reason: "is empty", Hint: "give a path or remove the setting"}
	}
	*dst = v.text
	return nil
}

func parseAddr(v value) (netip.AddrPort, *Error) {
	a, err := netip.ParseAddrPort(v.text)
	if err != nil {
		return netip.AddrPort{}, bad(v, "is not an IP address and port", "write it as 127.0.0.1:4566, or 127.0.0.1:0 for a free port")
	}
	return a, nil
}

func (d *draft) setListen(v value) *Error {
	a, err := parseAddr(v)
	d.listen, d.listenAt = &a, v.where
	return err
}

func (d *draft) setToken(v value) *Error {
	for _, r := range v.text {
		if r <= ' ' || r > '~' {
			return &Error{Where: v.where, Reason: "holds a character a bearer token cannot: spaces, control and non-ASCII characters are not allowed", Hint: "generate one with `openssl rand -hex 32`"}
		}
	}
	if v.text == "" {
		return &Error{Where: v.where, Reason: "is empty", Hint: "generate one with `openssl rand -hex 32`"}
	}
	s := Secret(v.text)
	d.token, d.tokenAt = &s, v.where
	return nil
}

func (d *draft) setAuth(v value) *Error {
	var a authChoice
	switch v.text {
	case "token":
		a = authToken
	case "container-published":
		a = authContainerPublished
	default:
		return bad(v, "is not token or container-published", "")
	}
	d.auth, d.authAt = &a, v.where
	return nil
}

func (d *draft) setAWSAPI(v value) *Error {
	b, err := strconv.ParseBool(v.text)
	if err != nil {
		return bad(v, "is not true or false", "")
	}
	d.awsAPI, d.awsAPIAt = b, v.where
	return nil
}

func (d *draft) setTLS(v value) *Error {
	b, err := strconv.ParseBool(v.text)
	if err != nil {
		return bad(v, "is not true or false", "")
	}
	d.tls, d.tlsAt = &b, v.where
	return nil
}

func (d *draft) setTLSListen(v value) *Error {
	a, err := parseAddr(v)
	d.tlsListen, d.tlsListenAt = &a, v.where
	return err
}

func (d *draft) setPersist(v value) *Error { return nonEmpty(v, &d.persist) }

func (d *draft) setEvery(v value) *Error {
	every, err := time.ParseDuration(v.text)
	if err != nil || every < time.Second {
		return bad(v, "is not a duration of at least 1s", "write it as a Go duration, such as 30s or 5m")
	}
	d.every, d.everyAt = &every, v.where
	return nil
}

func (d *draft) setSeed(v value) *Error {
	n, err := strconv.ParseUint(v.text, 10, 64)
	if err != nil {
		return bad(v, "is not an unsigned integer", "write it as 42")
	}
	d.cfg.Seed = &n
	return nil
}

func (d *draft) setClock(v value) *Error {
	switch {
	case v.text == "running":
		d.frozen = nil
	case v.text == "frozen":
		d.frozen = &Frozen{}
	case strings.HasPrefix(v.text, "frozen@"):
		at, err := time.Parse(time.RFC3339, strings.TrimPrefix(v.text, "frozen@"))
		if err != nil {
			return bad(v, "has a time after frozen@ that is not RFC 3339", "write it as frozen@2026-01-01T00:00:00Z")
		}
		d.frozen = &Frozen{At: at}
	default:
		return bad(v, "is not running, frozen or frozen@<RFC 3339 time>", "")
	}
	d.frozenAt = v.where
	return nil
}

func (d *draft) setScale(v value) *Error {
	f, err := strconv.ParseFloat(v.text, 64)
	if err != nil || math.IsNaN(f) || f <= 0 || f > MaxClockScale {
		return bad(v, "is not a number above 0 and at most 10000", "write it as 60 for a minute per second")
	}
	d.scale, d.scaleAt = &f, v.where
	return nil
}

// resolveClock combines --clock and --clock-scale: a scale runs the clock
// that fast from wall time, which a frozen start contradicts.
func (d *draft) resolveClock() *Error {
	switch {
	case d.frozen != nil && d.scale != nil:
		return &Error{
			Where:  d.scaleAt,
			Reason: "scales the clock while " + d.frozenAt + " freezes it; a scale applies only to a running clock",
			Hint:   "remove one of the two, and scale a frozen clock after start with 'understudy clock scale FACTOR'",
		}
	case d.frozen != nil:
		d.cfg.Clock = *d.frozen
	case d.scale != nil:
		d.cfg.Clock = Scaled{Factor: *d.scale}
	}
	return nil
}

func (d *draft) setSigV4(v value) *Error {
	switch v.text {
	case "parse":
		d.cfg.SigV4 = ParseOnly
	case "verify":
		d.cfg.SigV4 = Verify
	default:
		return bad(v, "is not parse or verify", "")
	}
	return nil
}

func (d *draft) setDocker(v value) *Error {
	docker, err := parseDocker(v.text)
	if err != "" {
		return bad(v, err, "write unix:///var/run/docker.sock, or disabled to run no container")
	}
	d.docker = docker
	return nil
}

// parseDocker reads disabled or a unix:// socket URL; the reason is empty
// when it is valid.
func parseDocker(text string) (docker Docker, reason string) {
	if text == "disabled" {
		return DockerDisabled{}, ""
	}
	path, ok := strings.CutPrefix(text, "unix://")
	if !ok {
		return nil, "is not disabled or a unix:// socket; understudy speaks to Docker over a unix socket only"
	}
	if !strings.HasPrefix(path, "/") {
		return nil, "does not name an absolute socket path"
	}
	return DockerSocket{Path: path}, ""
}

func (d *draft) setAccept(v value) *Error {
	if v.source == fromEnv {
		switch v.text {
		case "1":
			d.accept = true
		case "0":
			d.accept = false
		default:
			return bad(v, "is not 1 or 0; only the value 1 accepts the licence", "set "+AcceptLicenceEnv+"=1 to accept it")
		}
		d.acceptAt = v.where
		return nil
	}
	b, err := strconv.ParseBool(v.text)
	if err != nil {
		return bad(v, "is not true or false", "")
	}
	d.accept, d.acceptAt = b, v.where
	return nil
}

func (d *draft) setLogFormat(v value) *Error {
	switch v.text {
	case "text":
		d.cfg.Log.Format = LogText
	case "json":
		d.cfg.Log.Format = LogJSON
	default:
		return bad(v, "is not text or json", "")
	}
	return nil
}

func (d *draft) setLogLevel(v value) *Error {
	levels := map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}
	level, ok := levels[v.text]
	if !ok {
		return bad(v, "is not debug, info, warn or error", "")
	}
	d.cfg.Log.Level = level
	return nil
}

// finish resolves the settings that depend on each other and returns the
// configuration.
func (d *draft) finish() (Config, *Error) {
	if err := d.resolveListen(); err != nil {
		return Config{}, err
	}
	if err := d.resolveTLS(); err != nil {
		return Config{}, err
	}
	if err := d.resolveClock(); err != nil {
		return Config{}, err
	}
	if err := d.resolveHost(); err != nil {
		return Config{}, err
	}
	return d.cfg, nil
}

// resolveHost resolves the machine settings: persistence, Docker and the
// DynamoDB Local licence.
func (d *draft) resolveHost() *Error {
	if err := d.resolvePersistence(); err != nil {
		return err
	}
	d.resolveDocker()
	switch {
	case d.dynamoOff && d.accept:
		return &Error{Where: d.acceptAt, Reason: "accepts the DynamoDB Local licence while " + d.dynamoAt + " turns DynamoDB off", Hint: "remove one of the two"}
	case d.dynamoOff:
		d.cfg.DynamoDB.Licence = DynamoDBDisabled{}
	case d.accept:
		d.cfg.DynamoDB.Licence = LicenceAccepted{}
	}
	return nil
}

// resolveListen builds the listener from its address, the admin token,
// listenAuth and the unauthenticated AWS API acceptance. A token on
// loopback is refused rather than ignored; beyond loopback a token wins
// when the AWS API it does not guard is accepted as open, and without one
// the published container port is accepted only in a container with its
// own network namespace, outside Kubernetes.
func (d *draft) resolveListen() *Error {
	if err := d.resolveExposure(); err != nil {
		return err
	}
	// A token on loopback was refused above, so a token here guards a
	// listener beyond loopback.
	if d.awsAPI && d.token == nil {
		return &Error{Where: d.awsAPIAt, Reason: "opens the AWS API to the network, but no listener beyond loopback uses an admin token", Hint: "remove it, or set UNDERSTUDY_ADMIN_TOKEN and --listen 0.0.0.0:4566 to serve other machines"}
	}
	return nil
}

// minExposedTokenLength is the shortest admin token a listener beyond
// loopback accepts. Nothing rate-limits guesses, so a token any peer can
// try must be too long to enumerate; 16 hex characters are 64 bits.
const minExposedTokenLength = 16

// resolveExposure builds the listener from its address, the admin token
// and listenAuth.
func (d *draft) resolveExposure() *Error {
	if d.listen == nil && d.token == nil && d.auth == nil {
		d.warnLoopbackInContainer()
		return nil
	}
	addr := d.cfg.Listen.AddrPort()
	where := "the default listener " + addr.String()
	if d.listen != nil {
		addr, where = *d.listen, d.listenAt
	}
	if addr.Addr().IsLoopback() {
		if d.token != nil {
			return &Error{Where: d.tokenAt, Reason: "sets an admin token for a loopback listener (" + where + "), where every local process may call the admin API anyway", Hint: "remove the token, or listen beyond loopback"}
		}
		if d.auth != nil && *d.auth == authContainerPublished {
			return &Error{Where: d.authAt, Reason: "is container-published for a loopback listener (" + where + "), which no published port reaches", Hint: "listen on 0.0.0.0:4566 inside the container"}
		}
		d.cfg.Listen = Loopback{Addr: addr}
		return nil
	}
	if d.token != nil {
		if len(*d.token) < minExposedTokenLength {
			return &Error{
				Where:  d.tokenAt,
				Reason: "is shorter than the " + strconv.Itoa(minExposedTokenLength) + " characters a token guarding a listener beyond loopback (" + where + ") needs, since every peer that reaches it may guess without limit",
				Hint:   "generate one with `openssl rand -hex 32`",
			}
		}
		if !d.awsAPI {
			return &Error{
				Where:  d.tokenAt,
				Reason: "guards only the admin API under /_understudy/ of the listener beyond loopback (" + where + "); every client may sign its AWS API with the public dummy pair test/test, which verifies even under --sigv4 verify, so every peer that reaches it could read every secret and object and run any image as a Lambda function",
				Hint:   "add --" + AWSAPIFlag + " (UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API=true) on a network whose every peer you trust, or stay local with --listen 127.0.0.1:" + strconv.Itoa(int(addr.Port())),
			}
		}
		d.cfg.Listen = Exposed{Addr: addr, Auth: Token{Secret: *d.token}}
		return nil
	}
	auth := authToken
	if _, in := d.placement.(InContainer); in {
		auth = authContainerPublished
	}
	if d.auth != nil {
		auth = *d.auth
	}
	if auth == authToken {
		return &Error{
			Where:  where,
			Reason: "listens beyond loopback without an admin token, which would expose the admin API under /_understudy/ to the network",
			Hint:   "set UNDERSTUDY_ADMIN_TOKEN (every admin call then sends Authorization: Bearer <token>), or inside a container rely on the published port with --listen-auth container-published, or stay local with --listen 127.0.0.1:" + strconv.Itoa(int(addr.Port())),
		}
	}
	if reason := unpublished(d.placement); reason != "" {
		at := where
		if d.auth != nil {
			at = d.authAt
		}
		return &Error{
			Where:  at,
			Reason: "trusts a published container port, but " + reason,
			Hint:   "set UNDERSTUDY_ADMIN_TOKEN to listen beyond loopback here, or listen on 127.0.0.1",
		}
	}
	d.cfg.Listen = Exposed{Addr: addr, Auth: ContainerPublished{}}
	return nil
}

// warnLoopbackInContainer explains the loopback default of a container
// whose published port cannot guard 0.0.0.0, since a container usually
// listens beyond loopback.
func (d *draft) warnLoopbackInContainer() {
	switch d.placement.(type) {
	case HostNetworkContainer, InPod:
		addr := d.cfg.Listen.AddrPort()
		d.cfg.Warnings = append(d.cfg.Warnings, Warning{
			Setting: "--listen",
			Message: "the default listener is " + addr.String() + " rather than 0.0.0.0:" + strconv.Itoa(int(addr.Port())) + " because " + unpublished(d.placement),
			Fix:     "set UNDERSTUDY_ADMIN_TOKEN and --listen 0.0.0.0:" + strconv.Itoa(int(addr.Port())) + " to serve other machines, or pass --listen " + addr.String() + " to silence this warning",
		})
	case InContainer, OnHost:
	}
}

// resolveTLS turns --tls and --tls-listen into the TLS setting; the
// default TLS address is the main address at port 4567, or port 0 when the
// main port is 0, so parallel instances never collide.
func (d *draft) resolveTLS() *Error {
	if d.tls != nil && !*d.tls {
		if d.tlsListen != nil {
			return &Error{Where: d.tlsListenAt, Reason: "sets a TLS address while " + d.tlsAt + " turns TLS off", Hint: "remove one of the two"}
		}
		return nil
	}
	if d.tls == nil && d.tlsListen == nil {
		return nil
	}
	main := d.cfg.Listen.AddrPort()
	addr := netip.AddrPortFrom(main.Addr(), DefaultTLSPort)
	if main.Port() == 0 {
		addr = netip.AddrPortFrom(main.Addr(), 0)
	}
	if d.tlsListen != nil {
		addr = *d.tlsListen
		if _, loopback := d.cfg.Listen.(Loopback); loopback && !addr.Addr().IsLoopback() {
			return &Error{Where: d.tlsListenAt, Reason: "puts the TLS listener beyond loopback while the main listener stays on loopback; both serve the admin API", Hint: "use a loopback address such as 127.0.0.1:4567"}
		}
	}
	d.cfg.TLS = SelfSigned{Listen: addr}
	return nil
}

func (d *draft) resolvePersistence() *Error {
	if d.persist == "" {
		if d.every != nil {
			return &Error{Where: d.everyAt, Reason: "sets a checkpoint interval without a persistence directory", Hint: "add --persist <dir>, or remove the interval"}
		}
		return nil
	}
	every := DefaultSaveEvery
	if d.every != nil {
		every = *d.every
	}
	d.cfg.Persistence = Dir{Path: d.persist, SaveEvery: every}
	return nil
}

// resolveDocker uses an explicit docker setting, else DOCKER_HOST, else the
// default socket. DOCKER_HOST is Docker's setting, not understudy's: one
// understudy cannot use, such as tcp:// in GitLab's docker-in-docker or
// ssh://, turns containers off with a warning rather than refusing to
// serve, as an unreachable Docker does.
func (d *draft) resolveDocker() {
	switch {
	case d.docker != nil:
		d.cfg.Docker = d.docker
	case d.dockerHost != "":
		docker, reason := parseDocker(d.dockerHost)
		if _, off := docker.(DockerDisabled); reason != "" || off {
			d.cfg.Docker = DockerDisabled{}
			d.cfg.Warnings = append(d.cfg.Warnings, Warning{
				Setting: DockerHostEnv,
				Message: DockerHostEnv + " " + quote(d.dockerHost) + " is not a unix:// socket with an absolute path, the only kind understudy speaks to, so Lambda execution and DynamoDB are off",
				Fix:     "set UNDERSTUDY_DOCKER to a unix:// socket to use one, or to disabled to silence this warning",
			})
			return
		}
		d.cfg.Docker = docker
	}
}
