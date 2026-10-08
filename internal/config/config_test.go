package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func getenv(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

const fullConfig = `
mode: parallel
env_file: %ENV%
environments:
  INLINE: "${HOME_DIR}/inline"
  SHARED: inline
global:
  restic_repo: "s3:${BUCKET}"
  auto_prune: true
  prune_options: ["--cache-dir=${HOME_DIR}/cache"]
  pre_backup_cmd: /bin/global-pre
  post_backup_cmd: /bin/global-post
projects:
  web:
    service_manager: docker-compose
    compose_file: ${HOME_DIR}/web/compose.yml
    services: [web, db]
    sources: ["${HOME_DIR}/web"]
    stop_timeout: 30
    start_timeout: 60
    retention_policy: --keep-daily ${DAYS}
    backup_options: ["--exclude=${PATTERN}"]
    forget_options: ["--prune-${NOPE}"]
    pre_backup_cmd: /bin/web-pre
  units:
    service_manager: systemd
    systemd_units: [a.service]
    systemd_scope: user
    sources: [/etc/a]
    stop_timeout: 5
    start_timeout: 5
    stop_services: false
`

func loadFull(t *testing.T) *Config {
	t.Helper()
	dir := t.TempDir()
	envFile := writeFile(t, dir, "restor.env", "# comment\nFROM_FILE='file'\nSHARED=file\n")
	cfgFile := writeFile(t, dir, "config.yaml",
		strings.ReplaceAll(fullConfig, "%ENV%", envFile))
	cfg, err := Load(cfgFile, getenv(map[string]string{
		"HOME_DIR": "/home/x", "BUCKET": "bucket", "DAYS": "7", "PATTERN": "*.tmp",
	}))
	require.NoError(t, err)
	return cfg
}

func TestLoad_Full(t *testing.T) {
	t.Parallel()
	cfg := loadFull(t)

	assert.Equal(t, ModeParallel, cfg.RunMode())
	assert.Equal(t, "s3:bucket", cfg.Global.ResticRepo)
	assert.Equal(t, []string{"--cache-dir=/home/x/cache"}, cfg.Global.PruneOptions)
	assert.Equal(t, []string{"FROM_FILE=file", "INLINE=/home/x/inline", "SHARED=inline"},
		cfg.EnvPairs(), "names keep their case; inline variables win over the env file")

	projects := map[string]Project{}
	for _, p := range cfg.ResolvedProjects() {
		projects[p.Name] = p
	}
	assert.Equal(t, Project{
		Name: "web", ServiceManager: ManagerDockerCompose,
		ComposeFile: "/home/x/web/compose.yml", SystemdScope: ScopeSystem,
		Services: []string{"web", "db"}, Sources: []string{"/home/x/web"},
		StopTimeout: 30 * time.Second, StartTimeout: time.Minute,
		RetentionPolicy: "--keep-daily 7", StopServices: true,
		BackupOptions: []string{"--exclude=*.tmp"}, ForgetOptions: []string{"--prune-"},
		PreBackupCmd: "/bin/web-pre", PostBackupCmd: "/bin/global-post",
	}, projects["web"])
	assert.Equal(t, Project{
		Name: "units", ServiceManager: ManagerSystemd, SystemdUnits: []string{"a.service"},
		SystemdScope: ScopeUser, Sources: []string{"/etc/a"},
		StopTimeout: 5 * time.Second, StartTimeout: 5 * time.Second,
		PreBackupCmd: "/bin/global-pre", PostBackupCmd: "/bin/global-post",
	}, projects["units"])
}

func TestLoad_Defaults(t *testing.T) {
	t.Parallel()
	path := writeFile(t, t.TempDir(), "config.yaml", "projects: {}\n")
	cfg, err := Load(path, getenv(nil))
	require.NoError(t, err)
	assert.Equal(t, ModeSequential, cfg.Mode)
	assert.Empty(t, cfg.ResolvedProjects())
	assert.Empty(t, cfg.EnvPairs())
}

func TestLoad_ProjectNamesAreCaseInsensitive(t *testing.T) {
	t.Parallel()
	path := writeFile(t, t.TempDir(), "config.yaml", `
projects:
  WebApp: {service_manager: noop}
  Files: {service_manager: noop}
  files: {service_manager: noop}
`)
	_, err := Load(path, getenv(nil))
	require.ErrorIs(t, err, ErrInvalid)
	assert.Contains(t, err.Error(), "projects.files: duplicate project name")

	path = writeFile(t, t.TempDir(), "config.yaml", "projects:\n  WebApp: {service_manager: noop}\n")
	cfg, err := Load(path, getenv(nil))
	require.NoError(t, err)
	assert.Equal(t, "webapp", cfg.ResolvedProjects()[0].Name, "the restic tag stays lowercase")
}

func TestLoad_WeakTypes(t *testing.T) {
	t.Parallel()
	path := writeFile(t, t.TempDir(), "config.yaml", `
projects:
  a: {service_manager: noop, stop_timeout: "30", sources: "/a,/b", stop_services: "false"}
`)
	cfg, err := Load(path, getenv(nil))
	require.NoError(t, err)
	p := cfg.ResolvedProjects()[0]
	assert.Equal(t, 30*time.Second, p.StopTimeout)
	assert.Equal(t, []string{"/a", "/b"}, p.Sources)
	assert.False(t, p.StopServices)
}

func TestLoad_OtherFormats(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{
		"config.json": `{"mode":"parallel","global":{"restic_repo":"/repo"}}`,
		"config.toml": "mode = \"parallel\"\n[global]\nrestic_repo = \"/repo\"\n",
	}
	for name, content := range files {
		cfg, err := Load(writeFile(t, dir, name, content), getenv(nil))
		require.NoError(t, err, name)
		assert.Equal(t, ModeParallel, cfg.RunMode(), name)
		assert.Equal(t, "/repo", cfg.Global.ResticRepo, name)
	}
}

func TestLoad_Errors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tests := []struct {
		name, content string
		want          []string
	}{
		{name: "missing file", want: []string{"config: read"}},
		{name: "broken yaml", content: "mode: [", want: []string{"config: parse"}},
		{name: "wrong type", content: "projects: 5", want: []string{"config: parse"}},
		{
			name: "missing env file", content: "env_file: /nonexistent/restor.env",
			want: []string{"config: env file /nonexistent/restor.env"},
		},
		{
			name: "all validation problems",
			content: `
projects:
  a: {service_manager: docker-compose}
  b: {service_manager: systemd, systemd_scope: global}
  c: {}
  d: {service_manager: kubernetes}
  e: {service_manager: noop}
`,
			want: []string{
				"projects.a.compose_file: required for service_manager docker-compose",
				"projects.b.systemd_units: required for service_manager systemd",
				`projects.b.systemd_scope: must be "system" or "user"`,
				"projects.c.service_manager: required",
				`projects.d.service_manager: unsupported value "kubernetes"`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(dir, "missing.yaml")
			if tt.content != "" {
				path = writeFile(t, t.TempDir(), "config.yaml", tt.content)
			}
			_, err := Load(path, getenv(nil))
			require.Error(t, err)
			for _, w := range tt.want {
				assert.Contains(t, err.Error(), w)
			}
		})
	}
}

func TestValidate_FieldErrors(t *testing.T) {
	t.Parallel()
	cfg := &Config{Projects: map[string]ProjectConfig{"a": {}}}
	err := cfg.Validate()
	require.ErrorIs(t, err, ErrInvalid)
	var fe *FieldError
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, "projects.a.service_manager", fe.Path)
}

func TestRunMode(t *testing.T) {
	t.Parallel()
	for mode, want := range map[string]string{
		"": ModeSequential, "sequential": ModeSequential, "parallel": ModeParallel,
		"other": ModeSequential,
	} {
		assert.Equal(t, want, (&Config{Mode: mode}).RunMode(), mode)
	}
}

func TestResolvedProjects_SortedByName(t *testing.T) {
	t.Parallel()
	cfg := &Config{Projects: map[string]ProjectConfig{}}
	for _, name := range []string{"m", "c", "x", "a", "q", "b", "z", "k"} {
		cfg.Projects[name] = ProjectConfig{ServiceManager: ManagerNoop}
	}
	var names []string
	for _, p := range cfg.ResolvedProjects() {
		names = append(names, p.Name)
	}
	assert.Equal(t, []string{"a", "b", "c", "k", "m", "q", "x", "z"}, names)
}

func TestLoad_UnsupportedFormat(t *testing.T) {
	t.Parallel()
	_, err := Load(writeFile(t, t.TempDir(), "config.ini", "mode=parallel"), getenv(nil))
	require.ErrorIs(t, err, ErrFormat)
}

func TestLoad_EnvNamesKeepCaseInAllFormats(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{
		"config.yaml": "environments: {RESTIC_PASSWORD_FILE: /pw}\n",
		"config.json": `{"environments":{"RESTIC_PASSWORD_FILE":"/pw"}}`,
		"config.toml": "[environments]\nRESTIC_PASSWORD_FILE = \"/pw\"\n",
	}
	for name, content := range files {
		cfg, err := Load(writeFile(t, dir, name, content), getenv(nil))
		require.NoError(t, err, name)
		assert.Equal(t, []string{"RESTIC_PASSWORD_FILE=/pw"}, cfg.EnvPairs(), name)
	}
}
