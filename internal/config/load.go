package config

import (
	"errors"
	"flag"
	"io"
	"io/fs"
	"strings"
)

// source is where a setting's value came from.
type source uint8

const (
	fromFlag source = iota + 1
	fromEnv
	fromFile
)

// value is one setting's raw text and where it came from, spelled as that
// source spells the setting.
type value struct {
	text   string
	where  string
	source source
}

// OutputFlag is accepted by serve as another name for --log-format, since
// every understudy command takes --output.
const OutputFlag = "output"

// HelpHint is the Hint of a refused flag or argument (Where "--<flag>",
// "flags" or "arguments"): the next step is serve's own help, which a
// caller that already points there can leave out.
const HelpHint = "run 'understudy serve --help' for the flags"

// Load reads the serve configuration: flags in args over UNDERSTUDY_*
// variables in env ("NAME=value" pairs) over the JSON file (--config,
// UNDERSTUDY_CONFIG, or understudy.json when it exists) over the defaults.
// readFile reads the file and the container markers that decide the
// defaults and whether ContainerPublished is allowed. Every refusal is an
// *Error naming the setting as its source spells it.
func Load(args, env []string, readFile func(path string) ([]byte, error)) (Config, *Error) {
	d, err := load(args, env, readFile)
	if err != nil {
		return Config{}, err
	}
	return d.finish()
}

// LoadHost reads the configuration as Load does but resolves only the
// machine settings, for client commands such as doctor and cleanup. The
// listener rules are serve's: UNDERSTUDY_ADMIN_TOKEN, which is also the
// client's own credential, does not make LoadHost refuse a loopback
// listener.
func LoadHost(args, env []string, readFile func(path string) ([]byte, error)) (Host, *Error) {
	d, err := load(args, env, readFile)
	if err != nil {
		return Host{}, err
	}
	if err := d.resolveHost(); err != nil {
		return Host{}, err
	}
	return d.cfg.Host(), nil
}

// load applies every source to a draft of the defaults.
func load(args, env []string, readFile func(path string) ([]byte, error)) (*draft, *Error) {
	flags, configPath, err := parseFlags(args)
	if err != nil {
		return nil, err
	}
	envs, err := parseEnv(env)
	if err != nil {
		return nil, err
	}
	if configPath == nil {
		configPath = envs.configPath
	}
	file, err := readConfigFile(configPath, readFile)
	if err != nil {
		return nil, err
	}
	d := newDraft(DetectPlacement(readFile, env), envs.docker)
	setters := setters()
	for _, s := range Settings() {
		v, ok := flags[s.Key]
		if !ok {
			v, ok = envs.values[s.Key]
		}
		if !ok {
			v, ok = file.scalars[s.Key]
		}
		if !ok {
			continue
		}
		if err := setters[s.Key](d, v); err != nil {
			return nil, err
		}
	}
	if err := d.applySections(file.sections, file.name); err != nil {
		return nil, err
	}
	return d, nil
}

// parseFlags reads serve's flags into values by setting key, and the
// --config path when given.
func parseFlags(args []string) (map[string]value, *value, *Error) {
	values := map[string]value{}
	var configPath *value
	set := flag.NewFlagSet("serve", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	// The flag package flattens a setter's error into text, so a conflict
	// is kept here and returned whole.
	var conflict *Error
	record := func(key, name string) func(string) error {
		return func(text string) error {
			if prev, ok := values[key]; ok && prev.text != text && prev.where != "--"+name {
				conflict = &Error{Where: "--" + name, Reason: "conflicts with " + prev.where + ", which sets the same setting to another value", Hint: "pass only one of them"}
				return conflict
			}
			values[key] = value{text: text, where: "--" + name, source: fromFlag}
			return nil
		}
	}
	for _, s := range Settings() {
		switch {
		case s.Flag == "":
		case s.Switch():
			set.BoolFunc(s.Flag, s.Usage, record(s.Key, s.Flag))
		default:
			set.Func(s.Flag, s.Usage, record(s.Key, s.Flag))
		}
	}
	set.Func(OutputFlag, "another name for --log-format", record(keyLogFormat, OutputFlag))
	set.Func(ConfigFlag, "JSON configuration file", func(text string) error {
		configPath = &value{text: text, where: "--" + ConfigFlag, source: fromFlag}
		return nil
	})
	if err := checkFlags(args, set); err != nil {
		return nil, nil, err
	}
	if err := set.Parse(args); err != nil {
		if conflict != nil {
			return nil, nil, conflict
		}
		// checkFlags refuses every malformed flag the flag package does,
		// so this is a refusal it does not know yet; its text is the
		// flag package's.
		return nil, nil, &Error{Where: "flags", Reason: err.Error(), Hint: HelpHint}
	}
	if set.NArg() > 0 {
		return nil, nil, &Error{Where: "arguments", Reason: "unexpected argument " + quote(set.Arg(0)) + "; serve takes flags only", Hint: HelpHint}
	}
	return values, configPath, nil
}

// checkFlags refuses the first malformed flag in args before the flag
// package parses them, in words a reader can act on: one with more than
// two leading dashes, one set does not define (with the closest one it
// does), and one that takes a value but ends the arguments without it.
// The flag package would say "bad flag syntax", "flag provided but not
// defined" or "flag needs an argument: -listen".
func checkFlags(args []string, set *flag.FlagSet) *Error {
	var names []string
	set.VisitAll(func(f *flag.Flag) { names = append(names, f.Name) })
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || !strings.HasPrefix(a, "-") || a == "-" {
			return nil
		}
		if strings.HasPrefix(a, "---") {
			return &Error{Where: "flags", Reason: quote(a) + " is not a flag: a flag starts with - or --, such as --listen", Hint: HelpHint}
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		f := set.Lookup(name)
		if f == nil {
			err := &Error{Where: "--" + name, Reason: "is not a serve flag", Hint: HelpHint}
			if c := Closest(name, names); c != "" {
				err.Closest = "--" + c
			}
			return err
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); hasValue || (ok && bf.IsBoolFlag()) {
			continue
		}
		if i+1 == len(args) {
			return &Error{Where: "--" + name, Reason: "needs a value, as in --" + name + "=VALUE or --" + name + " VALUE", Hint: HelpHint}
		}
		i++
	}
	return nil
}

// envValues are the UNDERSTUDY_* settings of the environment, with the
// Docker CLI's variables that change the default Docker setting and say how
// to reach a tcp:// daemon. KUBERNETES_SERVICE_HOST is read by
// DetectPlacement.
type envValues struct {
	values     map[string]value
	configPath *value
	docker     dockerEnv
}

// envPrefix starts every variable understudy reads, bar the Docker CLI's,
// HOME and KubernetesEnv.
const envPrefix = "UNDERSTUDY_"

// parseEnv reads the UNDERSTUDY_* variables of env; an empty value is
// unset. A variable in that namespace that no setting reads is refused
// with the closest name, so a typo is not silently ignored.
func parseEnv(env []string) (envValues, *Error) {
	out := envValues{values: map[string]value{}}
	byName := map[string]string{}
	// The names after envPrefix, which alone tell two variables apart.
	known := make([]string, 0, len(Settings())+2)
	known = append(known, strings.TrimPrefix(ConfigEnv, envPrefix), strings.TrimPrefix(EndpointEnv, envPrefix))
	for _, s := range Settings() {
		byName[s.Env] = s.Key
		known = append(known, strings.TrimPrefix(s.Env, envPrefix))
	}
	for _, kv := range env {
		name, text, _ := strings.Cut(kv, "=")
		switch {
		case out.docker.read(name, text):
		case !strings.HasPrefix(name, envPrefix), text == "", name == EndpointEnv:
		case name == ConfigEnv:
			out.configPath = &value{text: text, where: ConfigEnv, source: fromEnv}
		default:
			key, ok := byName[name]
			if !ok {
				err := &Error{Where: name, Reason: "is not an understudy variable", Hint: "unset it or fix its name"}
				if c := Closest(strings.TrimPrefix(name, envPrefix), known); c != "" {
					err.Closest = envPrefix + c
				}
				return envValues{}, err
			}
			out.values[key] = value{text: text, where: name, source: fromEnv}
		}
	}
	return out, nil
}

// FileName is the configuration file args and env name, as Load reads
// them: --config, else UNDERSTUDY_CONFIG, else DefaultConfigFile. A caller
// names it in a refusal of a setting only the file holds, which a server
// makes once it applies the configuration Load accepted.
func FileName(args, env []string) string {
	_, path, err := parseFlags(args)
	if err == nil && path == nil {
		envs, eerr := parseEnv(env)
		if eerr == nil {
			path = envs.configPath
		}
	}
	if path == nil {
		return DefaultConfigFile
	}
	return path.text
}

// readConfigFile reads the file path names, or understudy.json when path
// is nil and that file exists.
func readConfigFile(path *value, readFile func(string) ([]byte, error)) (fileDoc, *Error) {
	name := DefaultConfigFile
	if path != nil {
		name = path.text
	}
	data, err := readFile(name)
	switch {
	case err == nil:
		return parseFile(name, data)
	case path == nil && errors.Is(err, fs.ErrNotExist):
		return fileDoc{name: name}, nil
	default:
		where := DefaultConfigFile
		if path != nil {
			where = path.where
		}
		return fileDoc{}, &Error{Where: where, Reason: "cannot read the configuration file " + quote(name) + ": " + err.Error(), Hint: "check the path, or remove the setting to run without a file"}
	}
}

func quote(s string) string { return `"` + s + `"` }
