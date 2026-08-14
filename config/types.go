package config

import (
	"time"
)

// Config represents the main configuration structure
type Config struct {
	Mode         string                   `mapstructure:"mode"`
	EnvFile      string                   `mapstructure:"env_file"`     // Optional
	Environments map[string]string        `mapstructure:"environments"` // Optional
	Global       GlobalConfig             `mapstructure:"global"`
	Projects     map[string]ProjectConfig `mapstructure:"projects"`
}

// GlobalConfig contains global settings
type GlobalConfig struct {
	ResticRepo    string   `mapstructure:"restic_repo"`
	AutoPrune     bool     `mapstructure:"auto_prune"`      // Global setting
	PruneOptions  []string `mapstructure:"prune_options"`   // Global restic options for prune command only
	PreBackupCmd  string   `mapstructure:"pre_backup_cmd"`  // Global pre-backup command
	PostBackupCmd string   `mapstructure:"post_backup_cmd"` // Global post-backup command
}

// ProjectConfig contains configuration for a single project
type ProjectConfig struct {
	ServiceManager  string   `mapstructure:"service_manager"`   // Required: "docker-compose", "podman-compose", or "systemd"
	ComposeFile     string   `mapstructure:"compose_file"`     // Required for docker-compose and podman-compose
	SystemdUnits    []string `mapstructure:"systemd_units"`    // Required for systemd
	SystemdScope    string   `mapstructure:"systemd_scope"`    // Optional for systemd: "system" (default) or "user"
	Services        []string `mapstructure:"services"`
	Sources         []string `mapstructure:"sources"`
	StopTimeout     int      `mapstructure:"stop_timeout"`     // Required per project
	StartTimeout    int      `mapstructure:"start_timeout"`    // Required per project
	RetentionPolicy string   `mapstructure:"retention_policy"` // Per project
	StopServices   *bool    `mapstructure:"stop_services"`   // Enable/disable service stop/start (null = true)
	BackupOptions   []string `mapstructure:"backup_options"`   // Per-project restic options for backup command only
	ForgetOptions   []string `mapstructure:"forget_options"`   // Per-project restic options for forget command only
	PreBackupCmd    string   `mapstructure:"pre_backup_cmd"`   // Per-project (overrides global)
	PostBackupCmd   string   `mapstructure:"post_backup_cmd"`  // Per-project (overrides global)
}

// Project represents a resolved project with all defaults applied
type Project struct {
	Name            string
	ServiceManager  string
	ComposeFile     string
	SystemdUnits    []string
	SystemdScope    string
	Services        []string
	Sources         []string
	ResticRepo      string
	StopTimeout     time.Duration
	StartTimeout    time.Duration
	RetentionPolicy string
	StopServices   bool              // Per project - enable/disable service stop/start (defaults to true)
	AutoPrune       bool              // From global settings
	BackupOptions   []string          // Per-project restic options for backup command only
	ForgetOptions   []string          // Per-project restic options for forget command only
	PruneOptions    []string          // Global prune options from global config
	PreBackupCmd    string            // Per-project (overrides global)
	PostBackupCmd   string            // Per-project (overrides global)
	Environment     map[string]string // Environment variables for this project
}

// GetMode returns the execution mode, defaults to "sequential" if not specified
func (c *Config) GetMode() string {
	if c.Mode == "parallel" || c.Mode == "sequential" {
		return c.Mode
	}
	return "sequential"
}

// HasProject checks if a project with the given name exists
func (c *Config) HasProject(name string) bool {
	_, exists := c.Projects[name]
	return exists
}

// GetProjectConfig returns the configuration for a specific project
func (c *Config) GetProjectConfig(name string) (ProjectConfig, bool) {
	project, exists := c.Projects[name]
	return project, exists
}
