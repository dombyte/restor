// Package config loads the restor configuration: one YAML file (format version 2) and
// the repository's env file. Load decodes the file strictly (an unknown key is an error,
// with a hint for the keys of version 1), expands ${VAR} references from the process
// environment, resolves relative paths against the file's directory, applies the
// defaults and validates everything once. The result is a read-only Plan.
package config

import (
	"fmt"
	"time"

	"go.yaml.in/yaml/v4"
)

// Version is the configuration format this package reads.
const Version = 2

// Job types.
const (
	// TypeFiles backs up files only; it is the type of a job without a type key.
	TypeFiles         = ""
	TypeDockerCompose = "docker-compose"
	TypePodmanCompose = "podman-compose"
	TypeSystemd       = "systemd"
)

// systemd scopes.
const (
	ScopeSystem = "system"
	ScopeUser   = "user"
)

// Defaults of settings that are not written.
const (
	DefaultConcurrency  = 1
	DefaultStopTimeout  = time.Minute
	DefaultStartTimeout = 2 * time.Minute
)

// File is the configuration file as written.
type File struct {
	// Version must be 2.
	Version int `yaml:"version"`
	// Concurrency is how many jobs run at once; 1 (default) runs them one after another,
	// 0 runs all at once.
	Concurrency *int `yaml:"concurrency"`
	// Repository is the restic repository all jobs back up to.
	Repository RepositoryConfig `yaml:"repository"`
	// OnFailure runs once after a run in which a job or the maintenance failed.
	OnFailure string `yaml:"on_failure"`
	// Defaults apply to every job that does not set the field itself.
	Defaults Defaults `yaml:"defaults"`
	// Jobs maps the job name (also the restic tag) to its settings.
	Jobs map[string]JobConfig `yaml:"jobs"`
}

// RepositoryConfig is the repository section.
type RepositoryConfig struct {
	URL            string            `yaml:"url"`
	EnvFile        string            `yaml:"env_file"`
	BackendOptions map[string]string `yaml:"backend_options"`
	LimitUpload    int               `yaml:"limit_upload"`   // KiB/s
	LimitDownload  int               `yaml:"limit_download"` // KiB/s
	RetryLock      Duration          `yaml:"retry_lock"`
	Prune          bool              `yaml:"prune"`
	MaxUnused      string            `yaml:"max_unused"`
}

// Defaults are the job settings that the defaults section can set. Pointers and nil
// lists mean "not set": a job's own value replaces the default, also for lists.
type Defaults struct {
	Stop              *bool     `yaml:"stop"`
	StopTimeout       *Duration `yaml:"stop_timeout"`
	StartTimeout      *Duration `yaml:"start_timeout"`
	BackupTimeout     *Duration `yaml:"backup_timeout"`
	Keep              *Keep     `yaml:"keep"`
	Exclude           []string  `yaml:"exclude"`
	IExclude          []string  `yaml:"iexclude"`
	ExcludeFile       []string  `yaml:"exclude_file"`
	ExcludeIfPresent  []string  `yaml:"exclude_if_present"`
	ExcludeCaches     *bool     `yaml:"exclude_caches"`
	ExcludeLargerThan *string   `yaml:"exclude_larger_than"`
	OneFileSystem     *bool     `yaml:"one_file_system"`
	SkipIfUnchanged   *bool     `yaml:"skip_if_unchanged"`
	Tags              []string  `yaml:"tags"`
	Pre               *string   `yaml:"pre"`
	Post              *string   `yaml:"post"`
	OnFailure         *string   `yaml:"on_failure"`
}

// JobConfig is one job as written: the fields of Defaults plus the job's own.
type JobConfig struct {
	Defaults    `yaml:",inline"`
	Type        string   `yaml:"type"`
	ComposeFile string   `yaml:"compose_file"`
	Units       []string `yaml:"units"`
	Scope       string   `yaml:"scope"`
	Services    []string `yaml:"services"`
	Sources     []string `yaml:"sources"`
	Before      []string `yaml:"before"`
	After       []string `yaml:"after"`
}

// Keep is a retention policy; the zero value keeps every snapshot (no forget).
type Keep struct {
	Last          int      `yaml:"last"`
	Hourly        int      `yaml:"hourly"`
	Daily         int      `yaml:"daily"`
	Weekly        int      `yaml:"weekly"`
	Monthly       int      `yaml:"monthly"`
	Yearly        int      `yaml:"yearly"`
	Within        string   `yaml:"within"`
	WithinHourly  string   `yaml:"within_hourly"`
	WithinDaily   string   `yaml:"within_daily"`
	WithinWeekly  string   `yaml:"within_weekly"`
	WithinMonthly string   `yaml:"within_monthly"`
	WithinYearly  string   `yaml:"within_yearly"`
	Tags          []string `yaml:"tags"`
}

// Duration is a time.Duration written as a string such as "30s" or "5m".
type Duration time.Duration

// UnmarshalYAML parses a Go duration; a number without unit is an error, so that "30"
// cannot be mistaken for seconds or nanoseconds.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf(`%q is not a duration such as "30s" or "5m"`, n.Value)
	}
	*d = Duration(v)
	return nil
}
