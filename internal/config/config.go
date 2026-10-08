package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v4"
)

var (
	// ErrInvalid is wrapped by every validation problem (see FieldError).
	ErrInvalid = errors.New("invalid")
	// ErrFormat is returned for a configuration file with an unsupported extension.
	ErrFormat = errors.New("unsupported format")
)

// FieldError is one validation problem, with the path of the field.
type FieldError struct {
	// Path is the dotted field path, e.g. "projects.web.compose_file".
	Path string
	// Problem says what is wrong.
	Problem string
}

func (e *FieldError) Error() string { return e.Path + ": " + e.Problem }

// Unwrap returns ErrInvalid.
func (e *FieldError) Unwrap() error { return ErrInvalid }

// Defaults returns the configuration before the file is applied.
func Defaults() *Config {
	return &Config{Mode: ModeSequential}
}

// Load reads the file at path, expands ${VAR} references with getenv, resolves relative
// paths against the file's directory, merges the environment and validates the result.
// All validation problems are returned together.
func Load(path string, getenv func(string) string) (*Config, error) {
	raw, err := readFile(path)
	if err != nil {
		return nil, err
	}
	cfg := Defaults()
	if err := decode(raw, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	dupErrs := cfg.lowerProjectNames()
	cfg.expandAll(getenv)
	if err := cfg.resolvePaths(path); err != nil {
		return nil, err
	}
	env, err := mergeEnvironments(cfg.EnvFile, cfg.Environments, getenv)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg.Environments = env
	if err := errors.Join(append(dupErrs, cfg.Validate())...); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// readFile parses the file by its extension (.yaml/.yml, .json, .toml) into a generic map;
// keys keep their case.
func readFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	raw := map[string]any{}
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".yaml", ".yml":
		err = yaml.Unmarshal(data, &raw)
	case ".json":
		err = json.Unmarshal(data, &raw)
	case ".toml":
		err = toml.Unmarshal(data, &raw)
	default:
		return nil, fmt.Errorf("config: %s: %w %q", path, ErrFormat, ext)
	}
	if err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return raw, nil
}

// decode maps raw onto cfg. Field names match case-insensitively, and scalars are
// converted loosely ("30" → 30, "a,b" → [a b]), as viper did before.
func decode(raw map[string]any, cfg *Config) error {
	d, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:           cfg,
		WeaklyTypedInput: true,
		DecodeHook:       mapstructure.StringToSliceHookFunc(","),
	})
	if err != nil {
		return err
	}
	return d.Decode(raw)
}

// lowerProjectNames makes project names lowercase: they are case-insensitive and the
// lowercase name is the restic tag (compatible with snapshots of earlier versions).
func (c *Config) lowerProjectNames() []error {
	var errs []error
	lower := make(map[string]ProjectConfig, len(c.Projects))
	for _, name := range sortedKeys(c.Projects) {
		key := strings.ToLower(name)
		if _, dup := lower[key]; dup {
			errs = append(errs, &FieldError{
				Path:    "projects." + key,
				Problem: "duplicate project name (names are case-insensitive)",
			})
		}
		lower[key] = c.Projects[name]
	}
	c.Projects = lower
	return errs
}

// EnvPairs returns the merged environment as sorted KEY=value pairs.
func (c *Config) EnvPairs() []string {
	pairs := make([]string, 0, len(c.Environments))
	for k, v := range c.Environments {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	return pairs
}

// RunMode returns Mode; an empty mode is ModeSequential.
func (c *Config) RunMode() string {
	if c.Mode == ModeParallel {
		return ModeParallel
	}
	return ModeSequential
}

// ResolvedProjects returns the projects with defaults and global hooks applied, sorted
// by name (the run order in sequential mode).
func (c *Config) ResolvedProjects() []Project {
	projects := make([]Project, 0, len(c.Projects))
	for _, name := range sortedKeys(c.Projects) {
		projects = append(projects, c.project(name, c.Projects[name]))
	}
	return projects
}

func (c *Config) project(name string, p ProjectConfig) Project {
	out := Project{
		Name: name, ServiceManager: p.ServiceManager, ComposeFile: p.ComposeFile,
		SystemdUnits: p.SystemdUnits, SystemdScope: p.SystemdScope, Services: p.Services,
		Sources: p.Sources, RetentionPolicy: p.RetentionPolicy, StopServices: true,
		StopTimeout:   time.Duration(p.StopTimeout) * time.Second,
		StartTimeout:  time.Duration(p.StartTimeout) * time.Second,
		BackupOptions: p.BackupOptions, ForgetOptions: p.ForgetOptions,
		PreBackupCmd: c.Global.PreBackupCmd, PostBackupCmd: c.Global.PostBackupCmd,
	}
	if out.SystemdScope == "" {
		out.SystemdScope = ScopeSystem
	}
	if p.StopServices != nil {
		out.StopServices = *p.StopServices
	}
	if p.PreBackupCmd != "" {
		out.PreBackupCmd = p.PreBackupCmd
	}
	if p.PostBackupCmd != "" {
		out.PostBackupCmd = p.PostBackupCmd
	}
	return out
}

// Validate checks every field and returns all problems joined.
func (c *Config) Validate() error {
	var errs []error
	if !slices.Contains([]string{"", ModeSequential, ModeParallel}, c.Mode) {
		errs = append(errs, &FieldError{
			Path: "mode", Problem: fmt.Sprintf("must be %q or %q", ModeSequential, ModeParallel),
		})
	}
	for _, name := range sortedKeys(c.Projects) {
		errs = append(errs, validateProject("projects."+name, c.Projects[name])...)
	}
	return errors.Join(errs...)
}

func validateProject(path string, p ProjectConfig) []error {
	v := &validator{path: path}
	v.check(len(p.Sources) > 0, "sources", "required")
	v.check(p.StopTimeout >= 0, "stop_timeout", "must be >= 0")
	v.check(p.StartTimeout >= 0, "start_timeout", "must be >= 0")
	if stopsServices(p) {
		v.check(p.StopTimeout != 0, "stop_timeout", "must be > 0 when services are stopped")
		v.check(p.StartTimeout != 0, "start_timeout", "must be > 0 when services are stopped")
	}
	v.serviceManager(p)
	return v.errs
}

// stopsServices reports whether the project stops services, so its timeouts matter.
func stopsServices(p ProjectConfig) bool {
	managed := []string{ManagerDockerCompose, ManagerPodmanCompose, ManagerSystemd}
	return slices.Contains(managed, p.ServiceManager) && (p.StopServices == nil || *p.StopServices)
}

// serviceManager checks the service manager and the fields it requires.
func (v *validator) serviceManager(p ProjectConfig) {
	switch p.ServiceManager {
	case ManagerDockerCompose, ManagerPodmanCompose:
		v.check(p.ComposeFile != "", "compose_file", "required for service_manager "+
			p.ServiceManager)
	case ManagerSystemd:
		v.check(len(p.SystemdUnits) > 0, "systemd_units", "required for service_manager systemd")
		v.check(slices.Contains([]string{"", ScopeSystem, ScopeUser}, p.SystemdScope),
			"systemd_scope", fmt.Sprintf("must be %q or %q", ScopeSystem, ScopeUser))
	case ManagerNoop:
	case "":
		v.check(false, "service_manager", "required")
	default:
		v.check(false, "service_manager", fmt.Sprintf("unsupported value %q", p.ServiceManager))
	}
}

// validator collects FieldErrors below one path.
type validator struct {
	path string
	errs []error
}

// check records problem for field unless ok.
func (v *validator) check(ok bool, field, problem string) {
	if !ok {
		v.errs = append(v.errs, &FieldError{Path: v.path + "." + field, Problem: problem})
	}
}

// expandAll replaces ${VAR} references in the project and global fields with getenv.
func (c *Config) expandAll(getenv func(string) string) {
	x := func(s string) string { return expand(s, getenv) }
	xs := func(list []string) {
		for i := range list {
			list[i] = x(list[i])
		}
	}
	c.EnvFile = x(c.EnvFile)
	c.Global.ResticRepo = x(c.Global.ResticRepo)
	c.Global.PreBackupCmd = x(c.Global.PreBackupCmd)
	c.Global.PostBackupCmd = x(c.Global.PostBackupCmd)
	xs(c.Global.PruneOptions)
	for name, p := range c.Projects {
		p.ServiceManager, p.ComposeFile = x(p.ServiceManager), x(p.ComposeFile)
		p.SystemdScope, p.RetentionPolicy = x(p.SystemdScope), x(p.RetentionPolicy)
		p.PreBackupCmd, p.PostBackupCmd = x(p.PreBackupCmd), x(p.PostBackupCmd)
		for _, list := range [][]string{
			p.SystemdUnits, p.Services, p.Sources, p.BackupOptions, p.ForgetOptions,
		} {
			xs(list)
		}
		c.Projects[name] = p
	}
}

// resolvePaths makes env_file, compose_file and sources absolute, relative to the
// directory of the configuration file at path: under systemd the working directory is /.
func (c *Config) resolvePaths(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	dir := filepath.Dir(abs)
	resolve := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}
	c.EnvFile = resolve(c.EnvFile)
	for name, p := range c.Projects {
		p.ComposeFile = resolve(p.ComposeFile)
		for i := range p.Sources {
			p.Sources[i] = resolve(p.Sources[i])
		}
		c.Projects[name] = p
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
