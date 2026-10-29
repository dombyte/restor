package restic

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// snapshotIDPattern matches a full or shortened snapshot ID.
const snapshotIDPattern = `^[0-9a-f]{8,64}$`

// ErrRestore is wrapped by the problems RestoreFlags.Validate reports.
var ErrRestore = errors.New("restic: invalid restore options")

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

// RestoreFlags are the options of restore.
type RestoreFlags struct {
	// Snapshot is the ID of the restored snapshot: at least 8 hex characters, never
	// "latest", which in a shared repository is the newest snapshot of any job and host.
	Snapshot string
	// Target is the directory the snapshot's absolute paths are restored below; "/"
	// restores them in place.
	Target string
	// DryRun only lists what restore would do, file by file.
	DryRun bool
	// Include, IInclude, Exclude and IExclude select the restored files by pattern.
	Include, IInclude, Exclude, IExclude []string
	// Overwrite is always, if-changed, if-newer or never; empty keeps restic's default.
	Overwrite string
	// Verify reads the restored files back.
	Verify bool
}

// Validate reports every problem of f, joined; each one wraps ErrRestore.
func (f RestoreFlags) Validate() error {
	var errs []error
	if !regexp.MustCompile(snapshotIDPattern).MatchString(f.Snapshot) {
		errs = append(errs, fmt.Errorf("%w: snapshot %q is not a snapshot ID (at least 8 "+
			"hex characters; find it with restor snapshots)", ErrRestore, f.Snapshot))
	}
	if f.Target == "" {
		errs = append(errs, fmt.Errorf("%w: no target directory", ErrRestore))
	}
	if f.Overwrite != "" && !slices.Contains(overwriteModes(), f.Overwrite) {
		errs = append(errs, fmt.Errorf("%w: overwrite %q is not one of %s", ErrRestore,
			f.Overwrite, strings.Join(overwriteModes(), ", ")))
	}
	patterns := slices.Concat(f.Include, f.IInclude, f.Exclude, f.IExclude)
	if slices.Contains(patterns, "") {
		errs = append(errs, fmt.Errorf("%w: empty include or exclude pattern", ErrRestore))
	}
	return errors.Join(errs...)
}

// Args returns the restic restore flags of f, the snapshot last after "--". A dry run
// is verbose, so that restic lists every file it would restore.
func (f RestoreFlags) Args() []string {
	args := []string{"--target=" + f.Target}
	if f.DryRun {
		args = append(args, "--dry-run", "--verbose=2")
	}
	args = appendEach(args, "--include", f.Include)
	args = appendEach(args, "--iinclude", f.IInclude)
	args = appendEach(args, "--exclude", f.Exclude)
	args = appendEach(args, "--iexclude", f.IExclude)
	if f.Overwrite != "" {
		args = append(args, "--overwrite="+f.Overwrite)
	}
	args = appendBool(args, "--verify", f.Verify)
	return append(args, "--", f.Snapshot)
}

// overwriteModes are the values of restore --overwrite.
func overwriteModes() []string { return []string{"always", "if-changed", "if-newer", "never"} }
