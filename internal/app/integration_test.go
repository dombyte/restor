//go:build integration

package app

// The integration tests run the restic found on PATH against local repositories and cover
// every restic feature restor uses, one subtest per feature, so a failing release shows
// what it lacks. They need no other tools and no root:
//
//	go test -tags integration -race ./internal/app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dombyte/restor/internal/command"
	"github.com/dombyte/restor/internal/restic"
)

// password contains characters that an expansion would change.
const password = "pa$word #1"

// repo is a fresh local repository.
type repo struct {
	dir    string
	env    []string
	runner *command.Runner
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	dir := t.TempDir()
	env := append(os.Environ(), "RESTIC_REPOSITORY="+filepath.Join(dir, "repo"),
		"RESTIC_PASSWORD="+password, "RESTIC_CACHE_DIR="+filepath.Join(dir, "cache"))
	r := &repo{dir: dir, env: env, runner: command.New(env, zerolog.Nop())}
	_, err := r.runner.Run(context.Background(), "restic", "init")
	require.NoError(t, err)
	return r
}

func (r *repo) client(t *testing.T, s restic.Settings) *restic.Client {
	t.Helper()
	c, err := restic.New(s, restic.Deps{Runner: r.runner, Log: zerolog.Nop()})
	require.NoError(t, err)
	return c
}

// snapshot is the part of `restic snapshots --json` the tests check.
type snapshot struct {
	ShortID string   `json:"short_id"`
	Tags    []string `json:"tags"`
	Paths   []string `json:"paths"`
}

func (r *repo) snapshots(t *testing.T, tag string) []snapshot {
	t.Helper()
	out, err := r.runner.Run(context.Background(), "restic", "snapshots", "--json",
		"--tag="+tag)
	require.NoError(t, err)
	var list []snapshot
	require.NoError(t, json.Unmarshal(out, &list))
	return list
}

// files returns the paths of the files in snapshot id, relative to root.
func (r *repo) files(t *testing.T, id, root string) []string {
	t.Helper()
	out, err := r.runner.Run(context.Background(), "restic", "ls", "--json", id)
	require.NoError(t, err)
	var files []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		var node struct{ Type, Path string }
		if json.Unmarshal(scanner.Bytes(), &node) == nil && node.Type == "file" {
			rel, err := filepath.Rel(root, node.Path)
			require.NoError(t, err)
			files = append(files, rel)
		}
	}
	slices.Sort(files)
	return files
}

func write(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func TestIntegration(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("restic", "version").Output()
	require.NoError(t, err, "restic must be on PATH")
	t.Logf("testing %s", strings.TrimSpace(string(out)))

	t.Run("version", testVersion)
	t.Run("backup flags", testBackupFlags)
	t.Run("global flags, prune and unlock", testGlobalFlags)
	t.Run("skip if unchanged", testSkipIfUnchanged)
	t.Run("forget", testForget)
	t.Run("incomplete snapshot", testIncomplete)
	t.Run("missing source", testMissingSource)
	t.Run("exit codes", testExitCodes)
	t.Run("restor run", testRestorRun)
	t.Run("restore", testRestore)
}

func testVersion(t *testing.T) {
	t.Parallel()
	v, err := newRepo(t).client(t, restic.Settings{}).Version(context.Background())
	require.NoError(t, err)
	assert.NotEqual(t, restic.Version{}, v)
}

func testBackupFlags(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	src := filepath.Join(r.dir, "src")
	for name, content := range map[string]string{
		"keep.txt": "keep", "sub/keep.txt": "keep", "x.tmp": "tmp", "UPPER.LOG": "log",
		"y.skip": "skip", "nobackup/.nobackup": "", "nobackup/f.txt": "f",
		"cache/CACHEDIR.TAG": "Signature: 8a477f597d28d172789f06886806bc55\n",
		"cache/f.txt":        "f", "big.bin": strings.Repeat("x", 4096),
	} {
		write(t, filepath.Join(src, name), content)
	}
	excludeFile := filepath.Join(r.dir, "excludes")
	write(t, excludeFile, "*.skip\n")
	flags := restic.BackupFlags{
		Exclude: []string{"*.tmp"}, IExclude: []string{"*.log"},
		ExcludeFile: []string{excludeFile}, ExcludeIfPresent: []string{".nobackup"},
		ExcludeCaches: true, ExcludeLargerThan: "1K", OneFileSystem: true,
		Tags: []string{"prod"},
	}

	id, err := r.client(t, restic.Settings{}).Backup(context.Background(), "web",
		[]string{src}, flags.Args())

	require.NoError(t, err)
	require.Len(t, id, 8)
	snaps := r.snapshots(t, "web")
	require.Len(t, snaps, 1)
	assert.Equal(t, id, snaps[0].ShortID)
	assert.ElementsMatch(t, []string{"web", "prod"}, snaps[0].Tags)
	assert.Equal(t, []string{
		"cache/CACHEDIR.TAG", "keep.txt", "nobackup/.nobackup", "sub/keep.txt",
	}, r.files(t, id, src), "every exclude option applied (the tag files stay)")
}

func testGlobalFlags(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	src := filepath.Join(r.dir, "src")
	write(t, filepath.Join(src, "a.txt"), "a")
	c := r.client(t, restic.Settings{
		GlobalArgs: restic.Repository{
			BackendOptions: map[string]string{"local.connections": "2"},
			LimitUpload:    10000, LimitDownload: 10000, RetryLock: time.Second,
		}.Args(),
		PruneArgs: restic.PruneFlags{MaxUnused: "5%"}.Args(),
	})
	ctx := context.Background()

	_, err := c.Backup(ctx, "web", []string{src}, nil)
	require.NoError(t, err)
	require.NoError(t, c.Forget(ctx, "web", restic.Keep{Last: 1}.Args()))
	require.NoError(t, c.Prune(ctx))
	require.NoError(t, c.Unlock(ctx))
}

// staticSource returns testdata/static. restic also records the directories above a
// source, so a backup is only unchanged while none of them changes: those of a temp dir
// change while other tests run, and even the home directory can change at any time, so
// the tests that expect a skipped backup retry it (unchangedWithin).
func staticSource(t *testing.T) string {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("testdata", "static"))
	require.NoError(t, err)
	return src
}

// unchangedWithin calls backup up to three times and reports whether one of the calls
// found nothing changed; a call that saved a snapshot (a directory above the source
// changed meanwhile) is tried again.
func unchangedWithin(t *testing.T, backup func() (unchanged bool)) bool {
	t.Helper()
	for range 3 {
		if backup() {
			return true
		}
	}
	return false
}

func testSkipIfUnchanged(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	src := staticSource(t)
	c := r.client(t, restic.Settings{})
	args := restic.BackupFlags{SkipIfUnchanged: true}.Args()
	_, err := c.Backup(context.Background(), "web", []string{src}, args)
	require.NoError(t, err)

	skipped := unchangedWithin(t, func() bool {
		id, err := c.Backup(context.Background(), "web", []string{src}, args)
		var unchanged *restic.UnchangedError
		if errors.As(err, &unchanged) {
			assert.Empty(t, id)
			return true
		}
		require.NoError(t, err)
		return false
	})

	assert.True(t, skipped, "a backup of unchanged files is skipped")
}

func testForget(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	c := r.client(t, restic.Settings{})
	ctx := context.Background()
	for _, dir := range []string{"one", "two", "three"} { // other sources each time
		src := filepath.Join(r.dir, dir)
		write(t, filepath.Join(src, "f.txt"), dir)
		_, err := c.Backup(ctx, "web", []string{src}, nil)
		require.NoError(t, err)
	}
	_, err := c.Backup(ctx, "other", []string{filepath.Join(r.dir, "one")}, nil)
	require.NoError(t, err)

	every := restic.Keep{
		Last: 9, Hourly: 9, Daily: 9, Weekly: 9, Monthly: 9, Yearly: 9, Within: "1d",
		WithinHourly: "1d", WithinDaily: "1d", WithinWeekly: "1m", WithinMonthly: "1y",
		WithinYearly: "2y", Tags: []string{"important"},
	}
	require.NoError(t, c.Forget(ctx, "web", every.Args()), "every keep option is accepted")
	assert.Len(t, r.snapshots(t, "web"), 3)

	require.NoError(t, c.Forget(ctx, "web", restic.Keep{Last: 1}.Args()))
	snaps := r.snapshots(t, "web")
	require.Len(t, snaps, 1, "one group per host, whatever the sources")
	assert.Contains(t, snaps[0].Paths[0], "three", "the latest is kept")
	assert.Len(t, r.snapshots(t, "other"), 1, "other jobs are not touched")
}

func testIncomplete(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("root reads every file")
	}
	r := newRepo(t)
	src := filepath.Join(r.dir, "src")
	write(t, filepath.Join(src, "a.txt"), "a")
	write(t, filepath.Join(src, "secret.txt"), "s")
	require.NoError(t, os.Chmod(filepath.Join(src, "secret.txt"), 0))

	id, err := r.client(t, restic.Settings{}).Backup(context.Background(), "web",
		[]string{src}, nil)

	var incomplete *restic.IncompleteError
	require.ErrorAs(t, err, &incomplete, "exit code 3")
	assert.Len(t, id, 8, "the snapshot was saved")
	assert.Contains(t, err.Error(), "permission denied", "the text of restic's JSON error")
	assert.NotContains(t, err.Error(), "message_type")
}

func testMissingSource(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	_, err := r.client(t, restic.Settings{}).Backup(context.Background(), "web",
		[]string{filepath.Join(r.dir, "missing")}, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Fatal: ", "the text of restic's JSON exit error")
	assert.NotContains(t, err.Error(), "message_type")
}

func testExitCodes(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	ctx := context.Background()

	missing := command.New(append(slices.Clone(r.env),
		"RESTIC_REPOSITORY="+filepath.Join(r.dir, "missing")), zerolog.Nop())
	c, err := restic.New(restic.Settings{}, restic.Deps{Runner: missing})
	require.NoError(t, err)
	assertExit(t, c.Prune(ctx), 10, "repository does not exist")

	wrong := command.New(append(slices.Clone(r.env), "RESTIC_PASSWORD=wrong"), zerolog.Nop())
	c, err = restic.New(restic.Settings{}, restic.Deps{Runner: wrong})
	require.NoError(t, err)
	assertExit(t, c.Prune(ctx), 12, "wrong password")

	// A backup from stdin holds a lock until its input ends; prune needs an exclusive one.
	holder := exec.Command("restic", "backup", "--stdin")
	holder.Env = r.env
	stdin, err := holder.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, holder.Start())
	defer func() {
		_ = stdin.Close()
		_ = holder.Wait()
	}()
	// Prune only once the lock exists: a prune that came first would lock the repository
	// exclusively and make the holder fail instead.
	require.Eventually(t, func() bool {
		locks, err := os.ReadDir(filepath.Join(r.dir, "repo", "locks"))
		return err == nil && len(locks) > 0
	}, 30*time.Second, 50*time.Millisecond, "the backup from stdin holds a lock")
	locked := r.client(t, restic.Settings{
		GlobalArgs: restic.Repository{RetryLock: time.Second}.Args(),
	})
	assertExit(t, locked.Prune(ctx), 11, "repository is locked")
}

// assertExit checks that err carries restic's exit code and restor's explanation of it.
func assertExit(t *testing.T, err error, code int, text string) {
	t.Helper()
	var exit interface{ ExitCode() int }
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, code, exit.ExitCode(), err.Error())
	assert.ErrorContains(t, err, text)
}

// testRestorRun runs a whole cycle through app.New and App.Run with every option of a
// files-only job and of the repository.
func testRestorRun(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	src := staticSource(t)
	// Quoted: " #" would start a comment. The $ stays literal.
	write(t, filepath.Join(r.dir, "restic.env"), "RESTIC_PASSWORD='"+password+"'\n")
	path := filepath.Join(r.dir, "config.yaml")
	write(t, path, `version: 2
concurrency: 0
repository:
  url: `+filepath.Join(r.dir, "repo")+`
  env_file: restic.env
  backend_options: {local.connections: "2"}
  limit_upload: 100000
  retry_lock: 1m
  prune: true
  max_unused: 10%
defaults:
  keep: {last: 2, within: 1d}
  exclude: ["*.tmp"]
  skip_if_unchanged: true
  backup_timeout: 5m
jobs:
  a: {sources: [`+src+`], before: [b], tags: [prod]}
  b: {sources: [`+src+`], one_file_system: true}
`)
	var environ []string
	for _, kv := range os.Environ() { // only the env file may give the password
		if !strings.HasPrefix(kv, "RESTIC_") {
			environ = append(environ, kv)
		}
	}
	o := Options{
		ConfigPath: path, Getenv: func(string) string { return "" }, Environ: environ,
		Log: zerolog.Nop(),
	}
	run := func() {
		a, err := New(o)
		require.NoError(t, err)
		require.NoError(t, a.Run(context.Background()))
	}
	run()
	first := r.snapshots(t, "a")
	require.Len(t, first, 1)
	assert.Equal(t, []string{"a.txt"}, r.files(t, first[0].ShortID, src))
	require.Len(t, r.snapshots(t, "b"), 1, "b has its own snapshot, although a has the "+
		"same sources")

	skipped := unchangedWithin(t, func() bool {
		before := len(r.snapshots(t, "a")) + len(r.snapshots(t, "b"))
		run()
		return len(r.snapshots(t, "a"))+len(r.snapshots(t, "b")) == before
	})

	assert.True(t, skipped, "a later run skips both jobs as unchanged")

	o.Jobs = []string{"b"}
	a, err := New(o)
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, a.Snapshots(context.Background(), Stdio{Out: &out, Err: &out}, false))
	assert.Contains(t, out.String(), r.snapshots(t, "b")[0].ShortID, "restor snapshots b")
	assert.NotContains(t, out.String(), first[0].ShortID, "not the snapshots of a")
}

// testRestore restores through app.New and App.Restore with a configuration that has
// only the repository: a dry run writes nothing, then --target with --include, then
// --in-place with --verify, all by short ID.
func testRestore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newRepo(t)
	src := filepath.Join(r.dir, "src")
	write(t, filepath.Join(src, "a.txt"), "a")
	write(t, filepath.Join(src, "sub", "b.txt"), "b")
	id, err := r.client(t, restic.Settings{}).Backup(ctx, "files", []string{src}, nil)
	require.NoError(t, err)
	path := filepath.Join(r.dir, "config.yaml")
	write(t, path, "version: 2\nrepository: {url: "+filepath.Join(r.dir, "repo")+"}\n")
	a, err := New(Options{
		ConfigPath: path, Getenv: func(string) string { return "" }, Environ: r.env,
		Log: zerolog.Nop(),
	})
	require.NoError(t, err)
	restore := func(o RestoreOptions) string {
		t.Helper()
		o.Snapshot = id[:8]
		var out bytes.Buffer
		require.NoError(t, a.Restore(ctx, Stdio{Out: &out, Err: &out}, o), out.String())
		return out.String()
	}

	target := filepath.Join(r.dir, "target")
	out := restore(RestoreOptions{Target: target})
	assert.NoDirExists(t, target, "a dry run writes nothing")
	assert.Contains(t, out, filepath.Join(src, "a.txt"), "the dry run lists the files")

	restore(RestoreOptions{
		Target: target, Include: []string{filepath.Join(src, "sub")},
		Apply: true,
	})
	assert.FileExists(t, filepath.Join(target, src, "sub", "b.txt"))
	assert.NoFileExists(t, filepath.Join(target, src, "a.txt"), "not included")

	write(t, filepath.Join(src, "a.txt"), "changed")
	restore(RestoreOptions{InPlace: true, Include: []string{src}, Apply: true, Verify: true})
	content, err := os.ReadFile(filepath.Join(src, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "a", string(content), "restored in place")

	err = a.Restore(ctx, Stdio{}, RestoreOptions{Snapshot: "latest", InPlace: true})
	require.ErrorIs(t, err, restic.ErrRestore)
}
