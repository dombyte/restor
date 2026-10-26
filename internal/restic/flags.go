package restic

import (
	"maps"
	"slices"
	"strconv"
	"time"
)

// Repository holds the settings that apply to every restic call. The repository itself
// and the credentials come from the environment.
type Repository struct {
	// BackendOptions are passed as --option=key=value, e.g. s3.connections=10.
	BackendOptions map[string]string
	// LimitUpload and LimitDownload limit the bandwidth in KiB/s; 0 is unlimited.
	LimitUpload, LimitDownload int
	// RetryLock waits that long for a locked repository; 0 fails at once.
	RetryLock time.Duration
}

// Args returns the global restic flags of r, the backend options sorted by key.
func (r Repository) Args() []string {
	var args []string
	for _, key := range slices.Sorted(maps.Keys(r.BackendOptions)) {
		args = append(args, "--option="+key+"="+r.BackendOptions[key])
	}
	args = appendInt(args, "--limit-upload", r.LimitUpload)
	args = appendInt(args, "--limit-download", r.LimitDownload)
	if r.RetryLock > 0 {
		args = append(args, "--retry-lock="+r.RetryLock.String())
	}
	return args
}

// BackupFlags are the options of one backup.
type BackupFlags struct {
	Exclude, IExclude, ExcludeFile, ExcludeIfPresent []string
	ExcludeCaches                                    bool
	// ExcludeLargerThan is a size such as "1G"; empty means no limit.
	ExcludeLargerThan string
	OneFileSystem     bool
	SkipIfUnchanged   bool
	// Tags are added to the snapshot next to the job's own tag.
	Tags []string
}

// Args returns the restic backup flags of f. Every value is passed as --flag=value, so
// it can never be read as a flag of its own.
func (f BackupFlags) Args() []string {
	var args []string
	args = appendEach(args, "--exclude", f.Exclude)
	args = appendEach(args, "--iexclude", f.IExclude)
	args = appendEach(args, "--exclude-file", f.ExcludeFile)
	args = appendEach(args, "--exclude-if-present", f.ExcludeIfPresent)
	args = appendBool(args, "--exclude-caches", f.ExcludeCaches)
	if f.ExcludeLargerThan != "" {
		args = append(args, "--exclude-larger-than="+f.ExcludeLargerThan)
	}
	args = appendBool(args, "--one-file-system", f.OneFileSystem)
	args = appendBool(args, "--skip-if-unchanged", f.SkipIfUnchanged)
	return appendEach(args, "--tag", f.Tags)
}

// Keep is a retention policy; the zero value keeps everything.
type Keep struct {
	Last, Hourly, Daily, Weekly, Monthly, Yearly int
	// Within… are restic durations such as "1y2m3d".
	Within, WithinHourly, WithinDaily, WithinWeekly, WithinMonthly, WithinYearly string
	// Tags keeps every snapshot with one of these tags.
	Tags []string
}

// Args returns the restic forget flags of k; none for the zero value.
func (k Keep) Args() []string {
	var args []string
	for _, n := range []struct {
		flag  string
		value int
	}{
		{"--keep-last", k.Last},
		{"--keep-hourly", k.Hourly},
		{"--keep-daily", k.Daily},
		{"--keep-weekly", k.Weekly},
		{"--keep-monthly", k.Monthly},
		{"--keep-yearly", k.Yearly},
	} {
		args = appendInt(args, n.flag, n.value)
	}
	for _, d := range []struct{ flag, value string }{
		{"--keep-within", k.Within},
		{"--keep-within-hourly", k.WithinHourly},
		{"--keep-within-daily", k.WithinDaily},
		{"--keep-within-weekly", k.WithinWeekly},
		{"--keep-within-monthly", k.WithinMonthly},
		{"--keep-within-yearly", k.WithinYearly},
	} {
		if d.value != "" {
			args = append(args, d.flag+"="+d.value)
		}
	}
	return appendEach(args, "--keep-tag", k.Tags)
}

// PruneFlags are the options of prune.
type PruneFlags struct {
	// MaxUnused is e.g. "5%", "1G" or "unlimited"; empty is restic's default.
	MaxUnused string
}

// Args returns the restic prune flags of f.
func (f PruneFlags) Args() []string {
	if f.MaxUnused == "" {
		return nil
	}
	return []string{"--max-unused=" + f.MaxUnused}
}

// SnapshotsFlags are the options of snapshots.
type SnapshotsFlags struct {
	// Tags select the snapshots that have any of them; empty selects all.
	Tags []string
}

// Args returns the restic snapshots flags of f.
func (f SnapshotsFlags) Args() []string {
	return appendEach(nil, "--tag", f.Tags)
}

func appendEach(args []string, flag string, values []string) []string {
	for _, v := range values {
		args = append(args, flag+"="+v)
	}
	return args
}

func appendInt(args []string, flag string, value int) []string {
	if value > 0 {
		args = append(args, flag+"="+strconv.Itoa(value))
	}
	return args
}

func appendBool(args []string, flag string, on bool) []string {
	if on {
		args = append(args, flag)
	}
	return args
}
