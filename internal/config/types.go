// Package config loads the restor configuration: one file (YAML, JSON or TOML, chosen by
// extension), an optional env file and inline environment variables. Keys keep their
// case (environment variable names are case-sensitive); project names are
// case-insensitive. Load applies the defaults, expands ${VAR} references from the process
// environment and validates everything once; the result is read-only.
package config

import "time"

// Execution modes.
const (
	ModeSequential = "sequential"
	ModeParallel   = "parallel"
)

// Service manager types.
const (
	ManagerDockerCompose = "docker-compose"
	ManagerPodmanCompose = "podman-compose"
	ManagerSystemd       = "systemd"
	ManagerNoop          = "noop"
)

// systemd scopes.
const (
	ScopeSystem = "system"
	ScopeUser   = "user"
)

// Config is the configuration file.
type Config struct {
	// Mode is ModeSequential or ModeParallel.
	Mode string `mapstructure:"mode"`
	// EnvFile is an optional .env, .yaml/.yml or .json file with environment variables.
	EnvFile string `mapstructure:"env_file"`
	// Environments are inline variables; after Load they also hold the env file's
	// variables (inline wins).
	Environments map[string]string `mapstructure:"environments"`
	// Global holds the repository-wide settings.
	Global GlobalConfig `mapstructure:"global"`
	// Projects maps the project name (also the restic tag) to its settings.
	Projects map[string]ProjectConfig `mapstructure:"projects"`
}

// GlobalConfig holds the settings shared by all projects.
type GlobalConfig struct {
	// ResticRepo is the repository all projects back up to.
	ResticRepo string `mapstructure:"restic_repo"`
	// AutoPrune runs forget per project and one prune after all backups.
	AutoPrune bool `mapstructure:"auto_prune"`
	// PruneOptions are global restic options for prune and unlock.
	PruneOptions []string `mapstructure:"prune_options"`
	// PreBackupCmd is the default pre-backup hook of every project.
	PreBackupCmd string `mapstructure:"pre_backup_cmd"`
	// PostBackupCmd is the default post-backup hook of every project.
	PostBackupCmd string `mapstructure:"post_backup_cmd"`
}

// ProjectConfig holds the settings of one project as written in the file.
type ProjectConfig struct {
	ServiceManager  string   `mapstructure:"service_manager"`
	ComposeFile     string   `mapstructure:"compose_file"`
	SystemdUnits    []string `mapstructure:"systemd_units"`
	SystemdScope    string   `mapstructure:"systemd_scope"`
	Services        []string `mapstructure:"services"`
	Sources         []string `mapstructure:"sources"`
	StopTimeout     int      `mapstructure:"stop_timeout"`  // seconds
	StartTimeout    int      `mapstructure:"start_timeout"` // seconds
	RetentionPolicy string   `mapstructure:"retention_policy"`
	StopServices    *bool    `mapstructure:"stop_services"` // nil = true
	BackupOptions   []string `mapstructure:"backup_options"`
	ForgetOptions   []string `mapstructure:"forget_options"`
	PreBackupCmd    string   `mapstructure:"pre_backup_cmd"`  // overrides the global hook
	PostBackupCmd   string   `mapstructure:"post_backup_cmd"` // overrides the global hook
}

// Project is a project with defaults and global hooks applied.
type Project struct {
	Name            string
	ServiceManager  string
	ComposeFile     string
	SystemdUnits    []string
	SystemdScope    string
	Services        []string
	Sources         []string
	StopTimeout     time.Duration
	StartTimeout    time.Duration
	RetentionPolicy string
	StopServices    bool
	BackupOptions   []string
	ForgetOptions   []string
	PreBackupCmd    string
	PostBackupCmd   string
}
