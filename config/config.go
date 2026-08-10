package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
	"go.yaml.in/yaml/v4"
)

// LoadConfig loads the configuration from the specified file
func LoadConfig(configPath string) (*Config, error) {
	// Set up viper
	v := viper.New()
	v.SetConfigFile(configPath)

	// Enable environment variable expansion
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.SetEnvPrefix("DOCKER_BACKUP")

	// Read the config file
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Unmarshal into Config struct
	var config Config
	if err := v.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Load and merge environment variables
	envVars, err := loadMergedEnvironments(config.EnvFile, config.Environments)
	if err != nil {
		return nil, fmt.Errorf("failed to load environments: %w", err)
	}

	// Return the config with merged environments
	return &Config{
		Mode:         config.Mode,
		EnvFile:      config.EnvFile,
		Environments: envVars,
		Global:       config.Global,
		Projects:     config.Projects,
	}, nil
}

// ToProjects converts the raw config to a slice of resolved Project structs
func (c *Config) ToProjects(envVars map[string]string) ([]Project, error) {
	var projects []Project

	for name, projectConfig := range c.Projects {
		project, err := c.resolveProject(name, projectConfig, envVars)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve project %s: %w", name, err)
		}
		projects = append(projects, *project)
	}

	return projects, nil
}

// resolveProject creates a resolved Project from ProjectConfig with defaults applied
func (c *Config) resolveProject(name string, projectConfig ProjectConfig, envVars map[string]string) (*Project, error) {
	project := &Project{
		Name:        name,
		Environment: make(map[string]string),
	}

	// Copy environment variables
	for k, v := range envVars {
		project.Environment[k] = v
	}

	// Apply defaults first, then override with project-specific values

	// ComposeFile (required per project, no default)
	project.ComposeFile = projectConfig.ComposeFile

	// Services
	project.Services = projectConfig.Services

	// Sources
	project.Sources = projectConfig.Sources

	// ResticRepo (global only, no per-project override)
	project.ResticRepo = c.Global.ResticRepo

	// StopTimeout (per project only, required)
	project.StopTimeout = time.Duration(projectConfig.StopTimeout) * time.Second

	// StartTimeout (per project only, required)
	project.StartTimeout = time.Duration(projectConfig.StartTimeout) * time.Second

	// RetentionPolicy (per project only)
	project.RetentionPolicy = projectConfig.RetentionPolicy

	// AutoPrune (from global settings)
	project.AutoPrune = c.Global.AutoPrune

	// StopContainers (per project only, defaults to true if not specified)
	project.StopContainers = true // Default to true
	if projectConfig.StopContainers != nil {
		project.StopContainers = *projectConfig.StopContainers
	}

	// BackupOptions - per-project only for backup command
	project.BackupOptions = projectConfig.BackupOptions

	// ForgetOptions - per-project only for forget command
	project.ForgetOptions = projectConfig.ForgetOptions

	// PruneOptions - from global config for prune command
	project.PruneOptions = c.Global.PruneOptions

	// PreBackupCmd (per-project overrides global)
	project.PreBackupCmd = c.Global.PreBackupCmd
	if projectConfig.PreBackupCmd != "" {
		project.PreBackupCmd = projectConfig.PreBackupCmd
	}

	// PostBackupCmd (per-project overrides global)
	project.PostBackupCmd = c.Global.PostBackupCmd
	if projectConfig.PostBackupCmd != "" {
		project.PostBackupCmd = projectConfig.PostBackupCmd
	}

	// Expand environment variables in project fields
	if err := expandProjectEnvVars(project); err != nil {
		return nil, err
	}

	return project, nil
}

// expandProjectEnvVars expands environment variables in project fields
func expandProjectEnvVars(project *Project) error {
	// Expand ComposeFile
	project.ComposeFile = os.ExpandEnv(project.ComposeFile)

	// Expand ResticRepo
	project.ResticRepo = os.ExpandEnv(project.ResticRepo)

	// Expand RetentionPolicy
	project.RetentionPolicy = os.ExpandEnv(project.RetentionPolicy)

	// Expand PreBackupCmd and PostBackupCmd
	project.PreBackupCmd = os.ExpandEnv(project.PreBackupCmd)
	project.PostBackupCmd = os.ExpandEnv(project.PostBackupCmd)

	// Expand environment variables in the Environment map
	for k, v := range project.Environment {
		project.Environment[k] = os.ExpandEnv(v)
	}

	// Expand sources
	for i, source := range project.Sources {
		project.Sources[i] = os.ExpandEnv(source)
	}

	// Expand backup options
	for i, option := range project.BackupOptions {
		project.BackupOptions[i] = os.ExpandEnv(option)
	}

	// Expand forget options
	for i, option := range project.ForgetOptions {
		project.ForgetOptions[i] = os.ExpandEnv(option)
	}

	// Expand prune options
	for i, option := range project.PruneOptions {
		project.PruneOptions[i] = os.ExpandEnv(option)
	}

	return nil
}

// loadMergedEnvironments loads environment variables from external file and merges with inline environments
// Inline environments take precedence over external file
func loadMergedEnvironments(envFile string, inlineEnv map[string]string) (map[string]string, error) {
	envVars := make(map[string]string)

	// Load external env file if specified
	if envFile != "" {
		fileEnv, err := loadEnvFile(envFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load env file %s: %w", envFile, err)
		}
		for k, v := range fileEnv {
			envVars[k] = v
		}
	}

	// Merge inline environments (takes precedence)
	for k, v := range inlineEnv {
		envVars[k] = os.ExpandEnv(v)
	}

	return envVars, nil
}

// loadEnvFile loads an environment file based on its extension
func loadEnvFile(filePath string) (map[string]string, error) {
	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("env file does not exist: %s", filePath)
	}

	ext := filepath.Ext(filePath)

	switch ext {
	case ".env":
		return loadDotEnvFile(filePath)
	case ".yaml", ".yml":
		return loadYamlEnvFile(filePath)
	case ".json":
		return loadJsonEnvFile(filePath)
	default:
		// Try .env format first, then YAML
		if _, err := os.Stat(filePath); err == nil {
			// File exists but unknown extension, try as .env format
			if env, err := loadDotEnvFile(filePath); err == nil {
				return env, nil
			}
			// Try as YAML
			if env, err := loadYamlEnvFile(filePath); err == nil {
				return env, nil
			}
			// Try as JSON
			return loadJsonEnvFile(filePath)
		}
		return nil, fmt.Errorf("unsupported env file format: %s", ext)
	}
}

// loadDotEnvFile loads a .env file (KEY=value format)
func loadDotEnvFile(filePath string) (map[string]string, error) {
	env := make(map[string]string)
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			// Remove quotes if present
			value = strings.Trim(value, `"'`)
			env[key] = os.ExpandEnv(value)
			log.Debug().Str("key", key).Str("value", value).Msg("Loaded env var from .env file")
		}
	}
	return env, scanner.Err()
}

// loadYamlEnvFile loads a YAML file with environment variables
func loadYamlEnvFile(filePath string) (map[string]string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var env map[string]string
	if err := yaml.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	// Expand values
	for k, v := range env {
		env[k] = os.ExpandEnv(v)
	}
	return env, nil
}

// loadJsonEnvFile loads a JSON file with environment variables
func loadJsonEnvFile(filePath string) (map[string]string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var env map[string]string
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	// Expand values
	for k, v := range env {
		env[k] = os.ExpandEnv(v)
	}
	return env, nil
}

// GetEnvArray converts environment map to array format for exec.Command
func GetEnvArray(envVars map[string]string) []string {
	var env []string
	// Start with current environment
	for _, e := range os.Environ() {
		env = append(env, e)
	}
	// Override with configured environment variables
	for k, v := range envVars {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	return env
}
