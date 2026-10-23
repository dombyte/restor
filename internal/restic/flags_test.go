package restic

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
