package restic

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepository_Args(t *testing.T) {
	t.Parallel()
	r := Repository{
		BackendOptions: map[string]string{"s3.connections": "10", "b2.connections": "4"},
		LimitUpload:    100, LimitDownload: 200, RetryLock: 10 * time.Minute,
	}
	assert.Equal(t, []string{
		"--option=b2.connections=4", "--option=s3.connections=10", "--limit-upload=100",
		"--limit-download=200", "--retry-lock=10m0s",
	}, r.Args())
	assert.Empty(t, Repository{}.Args())
}

func TestBackupFlags_Args(t *testing.T) {
	t.Parallel()
	f := BackupFlags{
		Exclude: []string{"*.tmp", "-weird"}, IExclude: []string{"*.LOG"},
		ExcludeFile: []string{"/etc/x"}, ExcludeIfPresent: []string{".nobackup"},
		ExcludeCaches: true, ExcludeLargerThan: "1G", OneFileSystem: true,
		SkipIfUnchanged: true, Tags: []string{"prod"},
	}
	assert.Equal(t, []string{
		"--exclude=*.tmp", "--exclude=-weird", "--iexclude=*.LOG", "--exclude-file=/etc/x",
		"--exclude-if-present=.nobackup", "--exclude-caches", "--exclude-larger-than=1G",
		"--one-file-system", "--skip-if-unchanged", "--tag=prod",
	}, f.Args())
	assert.Empty(t, BackupFlags{}.Args())
}

func TestKeep_Args(t *testing.T) {
	t.Parallel()
	k := Keep{
		Last: 1, Hourly: 2, Daily: 3, Weekly: 4, Monthly: 5, Yearly: 6,
		Within: "1d", WithinHourly: "2d", WithinDaily: "3d", WithinWeekly: "4m",
		WithinMonthly: "5y", WithinYearly: "6y", Tags: []string{"keep"},
	}
	assert.Equal(t, []string{
		"--keep-last=1", "--keep-hourly=2", "--keep-daily=3", "--keep-weekly=4",
		"--keep-monthly=5", "--keep-yearly=6", "--keep-within=1d",
		"--keep-within-hourly=2d", "--keep-within-daily=3d", "--keep-within-weekly=4m",
		"--keep-within-monthly=5y", "--keep-within-yearly=6y", "--keep-tag=keep",
	}, k.Args())
	assert.Empty(t, Keep{}.Args())
}

func TestPruneFlags_Args(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{"--max-unused=5%"}, PruneFlags{MaxUnused: "5%"}.Args())
	assert.Empty(t, PruneFlags{}.Args())
}

func TestSnapshotsFlags_Args(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{"--tag=db", "--tag=web"},
		SnapshotsFlags{Tags: []string{"db", "web"}}.Args())
	assert.Empty(t, SnapshotsFlags{}.Args())
}

func TestRestoreFlags_Args(t *testing.T) {
	t.Parallel()
	f := RestoreFlags{
		Snapshot: "1a2b3c4d", Target: "/tmp/r", DryRun: true, Include: []string{"/srv/a"},
		IInclude: []string{"*.DB"}, Exclude: []string{"-weird"}, IExclude: []string{"*.LOG"},
		Overwrite: "if-newer", Verify: true,
	}
	assert.Equal(t, []string{
		"--target=/tmp/r", "--dry-run", "--verbose=2", "--include=/srv/a", "--iinclude=*.DB",
		"--exclude=-weird", "--iexclude=*.LOG", "--overwrite=if-newer", "--verify", "--",
		"1a2b3c4d",
	}, f.Args())
	assert.Equal(t, []string{"--target=/", "--", "1a2b3c4d"},
		RestoreFlags{Snapshot: "1a2b3c4d", Target: "/"}.Args())
}

func TestRestoreFlags_Validate(t *testing.T) {
	t.Parallel()
	valid := RestoreFlags{Snapshot: "1a2b3c4d", Target: "/"}
	tests := []struct {
		name   string
		change func(*RestoreFlags)
		want   string
	}{
		{"latest", func(f *RestoreFlags) { f.Snapshot = "latest" }, `"latest" is not a snapshot`},
		{"short", func(f *RestoreFlags) { f.Snapshot = "1a2b3c" }, "not a snapshot ID"},
		{"upper case", func(f *RestoreFlags) { f.Snapshot = "1A2B3C4D" }, "not a snapshot ID"},
		{"flag", func(f *RestoreFlags) { f.Snapshot = "--help" }, "not a snapshot ID"},
		{"target", func(f *RestoreFlags) { f.Target = "" }, "no target directory"},
		{"overwrite", func(f *RestoreFlags) { f.Overwrite = "yes" }, `overwrite "yes"`},
		{"pattern", func(f *RestoreFlags) { f.Exclude = []string{""} }, "empty include"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := valid
			tt.change(&f)
			err := f.Validate()
			require.ErrorIs(t, err, ErrRestore)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
	require.NoError(t, valid.Validate())
	full := valid
	full.Snapshot, full.Overwrite = strings.Repeat("ab", 32), "never"
	require.NoError(t, full.Validate(), "a full ID")
}
