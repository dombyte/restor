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

// load writes content as config.yaml into a new directory and loads it.
func load(t *testing.T, content string, env map[string]string) (*Plan, error) {
	t.Helper()
	return Load(writeFile(t, t.TempDir(), "config.yaml", content), getenv(env))
}

const fullConfig = `
version: 2
concurrency: 2
repository:
  url: ${REPO}
  env_file: restic.env
  backend_options: {s3.connections: "10"}
  limit_upload: ${LIMIT}
  retry_lock: 10m
  prune: true
  max_unused: 5%
on_failure: /bin/notify run
defaults:
  stop_timeout: 30s
  keep: {daily: 7, weekly: 4}
  exclude: ["*.tmp"]
  on_failure: /bin/notify job
jobs:
  DB:
    type: docker-compose
    compose_file: app/compose.yml
    services: [db]
    sources: [data/db]
    before: [web]
  web:
    type: podman-compose
    compose_file: /srv/app/compose.yml
    sources: [/srv/app/data]
    exclude: []
    keep: {daily: 30, within: 1y2m}
    tags: [prod]
    pre: /bin/pre
    stop: false
  host:
    type: systemd
    units: [nginx.service, php.service]
    services: [nginx.service]
    scope: user
    sources: [/etc/nginx]
    after: [db]
    backup_timeout: 4h
  etc:
    sources: [/etc]
    exclude_larger_than: 1G
    one_file_system: true
    skip_if_unchanged: true
    exclude_caches: true
    exclude_if_present: [.nobackup]
    exclude_file: [excludes.txt]
    iexclude: ["*.LOG"]
`

func TestLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "restic.env", "RESTIC_PASSWORD='pa$word'\n")
	path := writeFile(t, dir, "config.yaml", fullConfig)

	plan, err := Load(path, getenv(map[string]string{"REPO": "/srv/repo", "LIMIT": "100"}))

	require.NoError(t, err)
	assert.Equal(t, 2, plan.Concurrency)
	assert.Equal(t, "/bin/notify run", plan.OnFailure)
	assert.Equal(t, Repository{
		URL: "/srv/repo", EnvFile: filepath.Join(dir, "restic.env"),
		Env:            []string{"RESTIC_PASSWORD=pa$word"},
		BackendOptions: map[string]string{"s3.connections": "10"}, LimitUpload: 100,
		RetryLock: 10 * time.Minute, Prune: true, MaxUnused: "5%",
	}, plan.Repository)

	require.Len(t, plan.Jobs, 4)
	db, etc, host, web := plan.Jobs[0], plan.Jobs[1], plan.Jobs[2], plan.Jobs[3]
	assert.Equal(t, Job{
		Name: "db", Type: TypeDockerCompose, ComposeFile: filepath.Join(dir, "app/compose.yml"),
		Scope: ScopeSystem, Services: []string{"db"}, Sources: []string{filepath.Join(dir, "data/db")},
		Stop: true, StopTimeout: 30 * time.Second, StartTimeout: DefaultStartTimeout,
		Keep: Keep{Daily: 7, Weekly: 4}, Exclude: []string{"*.tmp"}, OnFailure: "/bin/notify job",
	}, db)
	assert.True(t, db.StopsServices())

	assert.Equal(t, []string{"db"}, web.After, "db names web in before")
	assert.False(t, web.StopsServices())
	assert.Empty(t, web.Exclude, "an empty list replaces the default")
	assert.Equal(t, Keep{Daily: 30, Within: "1y2m"}, web.Keep, "keep replaces the default")
	assert.Equal(t, []string{"prod"}, web.Tags)
	assert.Equal(t, "/bin/pre", web.Pre)

	assert.Equal(t, []string{"db"}, host.After, "references are case-insensitive")
	assert.Equal(t, ScopeUser, host.Scope)
	assert.Equal(t, 4*time.Hour, host.BackupTimeout)

	assert.Equal(t, TypeFiles, etc.Type)
	assert.False(t, etc.StopsServices())
	assert.Equal(t, "1G", etc.ExcludeLargerThan)
	assert.True(t, etc.OneFileSystem && etc.SkipIfUnchanged && etc.ExcludeCaches)
	assert.Equal(t, []string{".nobackup"}, etc.ExcludeIfPresent)
	assert.Equal(t, []string{filepath.Join(dir, "excludes.txt")}, etc.ExcludeFile)
	assert.Equal(t, []string{"*.LOG"}, etc.IExclude)
}

func TestLoad_Defaults(t *testing.T) {
	t.Parallel()
	plan, err := load(t, "version: 2\njobs:\n  a:\n    type: systemd\n    units: [x]\n"+
		"    sources: [/a]\n", nil)

	require.NoError(t, err)
	assert.Equal(t, DefaultConcurrency, plan.Concurrency)
	assert.Empty(t, plan.Repository.Env)
	j := plan.Jobs[0]
	assert.True(t, j.Stop)
	assert.Equal(t, DefaultStopTimeout, j.StopTimeout)
	assert.Equal(t, DefaultStartTimeout, j.StartTimeout)
	assert.Zero(t, j.BackupTimeout)
	assert.Equal(t, Keep{}, j.Keep)
	assert.Empty(t, j.After)
}

func TestLoad_Version1Keys(t *testing.T) {
	t.Parallel()
	_, err := load(t, `
mode: parallel
env_file: x.env
environments: {A: b}
global: {restic_repo: /r}
projects:
  web:
    service_manager: docker-compose
    systemd_units: [a]
    retention_policy: --keep-last 1
    backup_options: [--x]
    stop_service: true
`, nil)

	require.ErrorIs(t, err, ErrInvalid)
	for _, want := range []string{
		"version: must be 2",
		"mode: unknown key (line 2): replaced by concurrency",
		"env_file: unknown key (line 3): moved to repository.env_file",
		"environments: unknown key (line 4): removed",
		"global: unknown key (line 5): replaced by repository and defaults",
		"projects: unknown key (line 6): renamed to jobs in configuration version 2",
		"jobs: at least one job is required",
	} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestLoad_UnknownJobKeys(t *testing.T) {
	t.Parallel()
	_, err := load(t, `version: 2
defaults:
  pre_backup_cmd: x
jobs:
  Web:
    sources: [/a]
    stop_service: true
    service_manager: noop
    keep: {days: 1}
    retention_policy: --keep-last 1
`, nil)

	require.ErrorIs(t, err, ErrInvalid)
	for _, want := range []string{
		"defaults.pre_backup_cmd: unknown key (line 3): renamed to pre",
		"jobs.web.stop_service: unknown key (line 7)\n",
		"jobs.web.service_manager: unknown key (line 8): renamed to type",
		"jobs.web.keep.days: unknown key (line 9)",
		"jobs.web.retention_policy: unknown key (line 10): replaced by keep",
	} {
		assert.Contains(t, err.Error()+"\n", want)
	}
}

func TestLoad_TypeErrors(t *testing.T) {
	t.Parallel()
	_, err := load(t, `version: 2
concurrency: two
defaults:
  stop_timeout: 30
jobs:
  Web:
    sources: /a
  db: {sources: [/b], tags: [x, 1], start_timeout: 5, keep: {daily: x}}
`, nil)

	require.ErrorIs(t, err, ErrInvalid)
	for _, want := range []string{
		"concurrency: cannot construct !!str `two` into int (line 2)",
		`defaults.stop_timeout: "30" is not a duration such as "30s" or "5m" (line 4)`,
		"jobs.web.sources: cannot construct !!str `/a` into []string (line 7)",
		`jobs.db.start_timeout: "5" is not a duration such as "30s" or "5m" (line 8)`,
		"jobs.db.keep.daily: cannot construct !!str `x` into int (line 8)",
	} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestLoad_Validation(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, config, want string }{
		{"concurrency", "concurrency: -1", "concurrency: must be >= 0"},
		{
			"backend key", "repository: {backend_options: {connections: '1'}}",
			`repository.backend_options: "connections" is not a backend option`,
		},
		{
			"backend value", "repository: {backend_options: {s3.connections: ''}}",
			"repository.backend_options.s3.connections: must not be empty",
		},
		{"limit", "repository: {limit_upload: -1}", "repository.limit_upload: must be >= 0"},
		{"retry lock", "repository: {retry_lock: -1s}", "repository.retry_lock: must be >= 0"},
		{"max unused", "repository: {max_unused: lots}", `repository.max_unused: "lots" is not`},
		{"keep count", "defaults: {keep: {daily: -1}}", "defaults.keep.daily: must be >= 0"},
		{"keep within", "defaults: {keep: {within: 30x}}", `defaults.keep.within: "30x" is not`},
		{"keep tag", "defaults: {keep: {tags: ['a,b']}}", `defaults.keep.tags: "a,b": a tag`},
		{"size", "defaults: {exclude_larger_than: big}", `defaults.exclude_larger_than: "big"`},
		{"empty exclude", "defaults: {exclude: ['']}", "defaults.exclude: must not contain"},
		{"tag", "defaults: {tags: ['-x']}", `defaults.tags: "-x": a tag must not`},
		{"duration", "defaults: {backup_timeout: -1m}", "defaults.backup_timeout: must be >= 0"},
		{"name", "jobs: {'-web': {sources: [/a]}}", `jobs.-web: name must not be empty`},
		{"sources", "jobs: {web: {type: systemd, units: [a]}}", "jobs.web.sources: required"},
		{"empty source", "jobs: {web: {sources: ['']}}", "jobs.web.sources: must not contain"},
		{"type", "jobs: {web: {type: noop, sources: [/a]}}", `jobs.web.type: unsupported value "noop"`},
		{
			"compose file", "jobs: {web: {type: docker-compose, sources: [/a]}}",
			"jobs.web.compose_file: required for type docker-compose",
		},
		{"compose units", "jobs: {web: {type: podman-compose, compose_file: c, units: [a], " +
			"sources: [/a]}}", "jobs.web.units: only for type systemd"},
		{"compose scope", "jobs: {web: {type: podman-compose, compose_file: c, scope: user, " +
			"sources: [/a]}}", "jobs.web.scope: only for type systemd"},
		{
			"units", "jobs: {web: {type: systemd, sources: [/a]}}",
			"jobs.web.units: required for type systemd",
		},
		{
			"scope", "jobs: {web: {type: systemd, units: [a], scope: root, sources: [/a]}}",
			`jobs.web.scope: must be "system" or "user"`,
		},
		{
			"services", "jobs: {web: {type: systemd, units: [a], services: [b], sources: [/a]}}",
			`jobs.web.services: "b" is not in units`,
		},
		{"systemd compose file", "jobs: {web: {type: systemd, units: [a], compose_file: c, " +
			"sources: [/a]}}", "jobs.web.compose_file: only for type docker-compose"},
		{
			"files stop", "jobs: {web: {stop: false, sources: [/a]}}",
			"jobs.web.stop: only for jobs with a type",
		},
		{
			"files services", "jobs: {web: {services: [a], sources: [/a]}}",
			"jobs.web.services: only for jobs with a type",
		},
		{"stop timeout", "jobs: {web: {type: systemd, units: [a], stop_timeout: 0s, " +
			"sources: [/a]}}", "jobs.web.stop_timeout: must be > 0 when services are stopped"},
		{"start timeout", "defaults: {start_timeout: 0s}\njobs: {web: {type: systemd, " +
			"units: [a], sources: [/a]}}", "jobs.web.start_timeout: must be > 0"},
		{
			"after", "jobs: {web: {after: [db], sources: [/a]}}",
			`jobs.web.after: "db" is not another job`,
		},
		{
			"before self", "jobs: {web: {before: [web], sources: [/a]}}",
			`jobs.web.before: "web" is not another job`,
		},
		{
			"duplicate", "jobs: {web: {sources: [/a]}, WEB: {sources: [/b]}}",
			"jobs.web: duplicate job name",
		},
		{
			"cycle", "jobs: {a: {after: [c], sources: [/a]}, b: {after: [a], sources: [/a]}, " +
				"c: {before: [b], after: [b], sources: [/a]}}",
			"jobs.a: before/after form a cycle: a → c → b → a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := "version: 2\n" + tt.config
			if !strings.Contains(tt.config, "jobs:") {
				config += "\njobs: {ok: {sources: [/a]}}"
			}
			_, err := load(t, config, nil)
			require.ErrorIs(t, err, ErrInvalid)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestLoad_StopFalseNeedsNoTimeouts(t *testing.T) {
	t.Parallel()
	_, err := load(t, "version: 2\njobs: {web: {type: systemd, units: [a], stop: false, "+
		"stop_timeout: 0s, sources: [/a]}}", nil)
	require.NoError(t, err)
}

func TestLoad_FileErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	_, err := Load(writeFile(t, dir, "config.json", "{}"), getenv(nil))
	require.ErrorIs(t, err, ErrFormat)

	_, err = Load(writeFile(t, dir, "empty.yaml", ""), getenv(nil))
	require.ErrorIs(t, err, ErrEmpty)

	_, err = Load(writeFile(t, dir, "broken.yml", "a: ["), getenv(nil))
	require.ErrorContains(t, err, "config: parse")

	_, err = Load(filepath.Join(dir, "missing.yaml"), getenv(nil))
	require.ErrorIs(t, err, os.ErrNotExist)

	_, err = load(t, "version: 2\nrepository: {env_file: /missing.env}\n"+
		"jobs: {a: {sources: [/a]}}", nil)
	require.ErrorContains(t, err, "repository.env_file: open /missing.env")

	path := writeFile(t, dir, "nul.env", "A=a\x00b\n")
	_, err = load(t, "version: 2\nrepository: {env_file: "+path+"}\n"+
		"jobs: {a: {sources: [/a]}}", nil)
	require.ErrorContains(t, err, "repository.env_file: A: contains a NUL byte")
}

func TestLoad_Expansion(t *testing.T) {
	t.Parallel()
	plan, err := load(t, `version: 2
repository: {url: "${REPO:-/default}", limit_download: $LIMIT}
jobs:
  web:
    sources: ["/$$HOME/x", "${DIR}/y"]
    pre: '${HOOK}'
`, map[string]string{"LIMIT": "50", "DIR": "/srv", "HOOK": "/bin/h"})

	require.NoError(t, err)
	assert.Equal(t, "/default", plan.Repository.URL)
	assert.Equal(t, 50, plan.Repository.LimitDownload, "a plain scalar is re-typed")
	assert.Equal(t, []string{"/$HOME/x", "/srv/y"}, plan.Jobs[0].Sources)
	assert.Equal(t, "/bin/h", plan.Jobs[0].Pre)
}

func TestPlan_Select(t *testing.T) {
	t.Parallel()
	plan := &Plan{Concurrency: 1, Jobs: []Job{{Name: "a"}, {Name: "b"}, {Name: "c"}}}

	all, err := plan.Select(nil)
	require.NoError(t, err)
	assert.Same(t, plan, all)

	some, err := plan.Select([]string{"C", "a"})
	require.NoError(t, err)
	assert.Equal(t, []Job{{Name: "a"}, {Name: "c"}}, some.Jobs)
	assert.Equal(t, 1, some.Concurrency)
	assert.Len(t, plan.Jobs, 3, "the plan is not changed")

	_, err = plan.Select([]string{"a", "x", "y"})
	require.ErrorIs(t, err, ErrUnknownJob)
	assert.EqualError(t, err, "config: unknown job: x, y")
}

func TestLoad_ExampleConfig(t *testing.T) {
	t.Parallel()
	plan, err := Load("../../example/config.yaml", getenv(nil))
	require.NoError(t, err, "example/config.yaml must stay valid")
	assert.NotEmpty(t, plan.Jobs)
}

func TestLoad_AnchorsAndMergeKeys(t *testing.T) {
	t.Parallel()
	plan, err := load(t, `version: 2
x-compose: &compose
  type: docker-compose
  compose_file: /srv/app/compose.yml
  stop_timeout: 10s
x-files: &files {sources: [/srv/app/data]}
jobs:
  db:
    <<: [*compose, *files]
    services: [db]
  web:
    <<: *compose
    sources: [/srv/app/web]
`, nil)

	require.NoError(t, err)
	db, web := plan.Jobs[0], plan.Jobs[1]
	assert.Equal(t, TypeDockerCompose, db.Type)
	assert.Equal(t, []string{"/srv/app/data"}, db.Sources)
	assert.Equal(t, 10*time.Second, web.StopTimeout)
	assert.Equal(t, "/srv/app/compose.yml", web.ComposeFile)
}

func TestLoad_UnknownKeysInMergedMappings(t *testing.T) {
	t.Parallel()
	_, err := load(t, `version: 2
x-common: &common {stop_service: true}
x-list: &list [{sources: [/a]}]
jobs:
  web:
    <<: *common
    sources: [/a]
  db: *common
nested:
  x-not-top: 1
`, nil)

	require.ErrorIs(t, err, ErrInvalid)
	for _, want := range []string{
		"jobs.web.stop_service: unknown key (line 2)",
		"jobs.db.stop_service: unknown key (line 2)",
		"nested: unknown key",
	} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestLoad_SafetyChecks(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, config, want string }{
		{
			"tag is a job", "jobs: {web: {sources: [/a], tags: [DB]}, db: {sources: [/b]}}",
			`jobs.web.tags: "DB" is the name of a job`,
		},
		{
			"default tag is a job", "defaults: {tags: [web]}\njobs: {web: {sources: [/a]}}",
			`defaults.tags: "web" is the name of a job`,
		},
		{
			"keep tags alone", "defaults: {keep: {tags: [important]}}\njobs: {a: {sources: [/a]}}",
			"defaults.keep.tags: needs another rule",
		},
		{
			"home source", "jobs: {A: {sources: [~/data]}}",
			`jobs.a.sources: "~/data": ~ is not expanded`,
		},
		{
			"home env file", "repository: {env_file: ~/restic.env}\njobs: {a: {sources: [/a]}}",
			`repository.env_file: "~/restic.env": ~ is not expanded`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := load(t, "version: 2\n"+tt.config, nil)
			require.ErrorIs(t, err, ErrInvalid)
			assert.Contains(t, err.Error(), tt.want)
		})
	}

	_, err := load(t, "version: 2\njobs: {a: {sources: [/a], keep: {daily: 7, tags: [x]}}}",
		nil)
	require.NoError(t, err, "tags next to another rule are fine")
}
