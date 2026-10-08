package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// ErrInvalid is wrapped by every validation problem (see FieldError).
var ErrInvalid = errors.New("invalid")

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

// Load reads the file at path, merges the environment, expands ${VAR} references with
// getenv and validates the result. All validation problems are returned together.
func Load(path string, getenv func(string) string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.SetEnvPrefix("DOCKER_BACKUP")
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	cfg := Defaults()
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	env, err := mergeEnvironments(cfg.EnvFile, cfg.Environments, getenv)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg.Environments = env
	cfg.expand(getenv)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
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

// RunMode returns Mode, falling back to ModeSequential for unknown values.
func (c *Config) RunMode() string {
	if c.Mode == ModeParallel {
		return ModeParallel
	}
	return ModeSequential
}

// ResolvedProjects returns the projects with defaults and global hooks applied.
func (c *Config) ResolvedProjects() []Project {
	projects := make([]Project, 0, len(c.Projects))
	for name, p := range c.Projects {
		projects = append(projects, c.project(name, p))
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
	for _, name := range sortedKeys(c.Projects) {
		errs = append(errs, validateProject("projects."+name, c.Projects[name])...)
	}
	return errors.Join(errs...)
}

func validateProject(path string, p ProjectConfig) []error {
	v := &validator{path: path}
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
	return v.errs
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

// expand replaces ${VAR} references in the project and global fields with getenv.
func (c *Config) expand(getenv func(string) string) {
	x := func(s string) string { return os.Expand(s, getenv) }
	xs := func(list []string) {
		for i := range list {
			list[i] = x(list[i])
		}
	}
	c.Global.ResticRepo = x(c.Global.ResticRepo)
	c.Global.PreBackupCmd = x(c.Global.PreBackupCmd)
	c.Global.PostBackupCmd = x(c.Global.PostBackupCmd)
	xs(c.Global.PruneOptions)
	for name, p := range c.Projects {
		p.ServiceManager, p.ComposeFile = x(p.ServiceManager), x(p.ComposeFile)
		p.SystemdScope, p.RetentionPolicy = x(p.SystemdScope), x(p.RetentionPolicy)
		p.PreBackupCmd, p.PostBackupCmd = x(p.PreBackupCmd), x(p.PostBackupCmd)
		for _, list := range [][]string{
			p.SystemdUnits, p.Sources, p.BackupOptions, p.ForgetOptions,
		} {
			xs(list)
		}
		c.Projects[name] = p
	}
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
