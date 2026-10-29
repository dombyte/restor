// Package main is the restor CLI. `restor run` runs one backup cycle and exits 0 when
// every job was backed up, 1 otherwise; `restor validate` checks the configuration,
// `restor snapshots` lists the snapshots of the jobs, `restor restore` restores a snapshot
// (a dry run unless --apply is given), `restor restic …` runs restic against the
// configured repository and `restor version` (or --version) prints the build
// information.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/dombyte/restor/internal/app"
)

// Build information, set by the Makefile and goreleaser via -ldflags "-X main.Version=…".
// -X can only set package-level string variables, hence the only globals in the binary.
//
//nolint:gochecknoglobals // written by the linker at build time, read-only afterwards
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
	GoVersion = "unknown"
)

var (
	// errReported marks an error that was already logged.
	errReported = errors.New("reported")
	// errNoCommand is returned when restor is called without a command, as version 1
	// was: running nothing must not look like a successful backup.
	errNoCommand = errors.New(`no command given: use "restor run" to back up ` +
		`(see "restor --help")`)
	// errShutdownDeadline is returned when the run outlives its shutdown timeout after a
	// signal.
	errShutdownDeadline = errors.New("shutdown deadline exceeded")
	// errNoConfig is returned when a user other than root has no home directory to find
	// the default configuration file in.
	errNoConfig = errors.New("no default configuration file: HOME is not set, " +
		"pass --config <file>")
	// errAllWithJobs is returned by `restor snapshots --all` with job names.
	errAllWithJobs = errors.New("--all lists every snapshot: name no jobs with it")
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the CLI with args and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	root := newRootCommand(app.DefaultConfig(os.Getuid(), os.Getenv), stdout, stderr)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		var exit interface{ ExitCode() int }
		switch {
		case errors.As(err, &exit) && exit.ExitCode() > 0:
			return exit.ExitCode() // restor restic: restic reported its error itself
		case !errors.Is(err, errReported):
			_, _ = fmt.Fprintln(stderr, "Error:", err)
		}
		return 1
	}
	return 0
}

func buildInfo() string {
	return fmt.Sprintf("restor %s (commit %s, built %s, %s)", Version, Commit, BuildDate,
		GoVersion)
}

// flags are the options every command shares.
type flags struct {
	config string
	debug  bool
	// defaultConfig is the configuration file used without --config.
	defaultConfig string
}

func newRootCommand(defaultConfig string, stdout, stderr io.Writer) *cobra.Command {
	f := &flags{defaultConfig: defaultConfig}
	root := &cobra.Command{
		Use:   "restor",
		Short: "Restic backup orchestrator",
		Long: `restor orchestrates restic backups of several jobs on one host.

It stops the services of each job (Docker/Podman Compose, systemd), backs up
its sources, starts the services again and applies the retention policies.`,
		Version:       Version,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE:          func(*cobra.Command, []string) error { return errNoCommand },
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetVersionTemplate(buildInfo() + "\n")
	root.PersistentFlags().StringVarP(&f.config, "config", "c", defaultConfig,
		"configuration file")
	root.PersistentFlags().BoolVar(&f.debug, "debug", false, "enable debug logging")
	root.AddCommand(newRunCommand(f, stderr), newValidateCommand(f, stdout, stderr),
		newSnapshotsCommand(f, stdout, stderr), newRestoreCommand(f, stdout, stderr),
		newResticCommand(f, stdout, stderr),
		&cobra.Command{
			Use:   "version",
			Short: "Print the build information",
			Args:  cobra.NoArgs,
			Run:   func(cmd *cobra.Command, _ []string) { cmd.Println(buildInfo()) },
		})
	return root
}

func newRunCommand(f *flags, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "run [job...]",
		Short: "Run one backup cycle of all jobs, or of the named ones",
		Long: `Run one backup cycle: back up every job (or only the named ones, in the order
their before/after allows), then forget and prune. Exits 1 when a job failed.`,
		RunE: func(cmd *cobra.Command, jobs []string) error {
			return runBackup(cmd.Context(), f, jobs,
				newLogger(stderr, level(f.debug, zerolog.InfoLevel)))
		},
	}
}

func newValidateCommand(f *flags, stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Check the configuration and print the jobs in their start order",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			log := newLogger(stderr, level(f.debug, zerolog.WarnLevel))
			a, err := newApp(f, nil, log)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(stdout, "%s is valid\n\n", f.config)
			return a.Describe(stdout)
		},
	}
}

func newSnapshotsCommand(f *flags, stdout, stderr io.Writer) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "snapshots [job...]",
		Short: "List the snapshots of all jobs, or of the named ones",
		Long: `List the snapshots of all jobs, or of the named ones: restic snapshots with
the job names as tags. --all, or a configuration without jobs, lists every snapshot of
the repository, also those of renamed or removed jobs. Use "restor restic snapshots" for
other options.`,
		RunE: func(cmd *cobra.Command, jobs []string) error {
			if all && len(jobs) > 0 {
				return errAllWithJobs
			}
			log := newLogger(stderr, level(f.debug, zerolog.WarnLevel))
			a, err := newApp(f, jobs, log)
			if err != nil {
				return err
			}
			stdio := app.Stdio{In: os.Stdin, Out: stdout, Err: stderr}
			return a.Snapshots(cmd.Context(), stdio, all)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "list every snapshot, not only those of the jobs")
	return cmd
}

func newRestoreCommand(f *flags, stdout, stderr io.Writer) *cobra.Command {
	var o app.RestoreOptions
	cmd := &cobra.Command{
		Use:   "restore <snapshot-ID> (--target <dir> | --in-place) [--apply]",
		Short: "Restore a snapshot (a dry run unless --apply is given)",
		Long: `Restore the files of one snapshot, found with "restor snapshots" (the short ID
works). Without --apply it is a dry run that lists what would be restored. --target
restores the snapshot's absolute paths below a directory (--target /srv/db gives
/srv/db/<original path>); --in-place restores to the original paths, the same as
--target /. Only files are restored:
stop the services that use them, and restor.timer, before an in-place restore.`,
		Example: `  restor restore 1a2b3c4d --target /tmp/restore
  restor restore 1a2b3c4d --in-place --include /srv/app/db --apply`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.Snapshot = args[0]
			log := newLogger(stderr, level(f.debug, zerolog.InfoLevel))
			a, err := newApp(f, nil, log)
			if err != nil {
				return err
			}
			stdio := app.Stdio{In: os.Stdin, Out: stdout, Err: stderr}
			if err := a.Restore(cmd.Context(), stdio, o); err != nil {
				log.Error().Err(err).Msg("restore failed")
				return reported(err)
			}
			return nil
		},
	}
	restoreFlags(cmd, &o)
	return cmd
}

func restoreFlags(cmd *cobra.Command, o *app.RestoreOptions) {
	fl := cmd.Flags()
	fl.StringVar(&o.Target, "target", "", "restore below this directory")
	fl.BoolVar(&o.InPlace, "in-place", false,
		"restore to the original paths (the same as --target /)")
	fl.BoolVar(&o.Apply, "apply", false, "write the files (default: dry run)")
	fl.StringArrayVar(&o.Include, "include", nil, "restore only files matching a pattern")
	fl.StringArrayVar(&o.IInclude, "iinclude", nil, "like --include, ignoring case")
	fl.StringArrayVar(&o.Exclude, "exclude", nil, "skip files matching a pattern")
	fl.StringArrayVar(&o.IExclude, "iexclude", nil, "like --exclude, ignoring case")
	fl.StringVar(&o.Overwrite, "overwrite", "",
		"overwrite existing files: always (restic's default), if-changed, if-newer or never")
	fl.BoolVar(&o.Verify, "verify", false, "read the restored files back")
}

// reported returns err when it carries an exit code (main exits with it), else
// errReported: the caller logged err already.
func reported(err error) error {
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) && exit.ExitCode() > 0 {
		return err
	}
	return errReported
}

func newResticCommand(f *flags, stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restic [restic arguments...]",
		Short: "Run restic with the configured repository and env file",
		Example: `  restor restic snapshots --tag web
  restor restic restore latest --tag web --target /tmp/restore`,
		RunE: func(cmd *cobra.Command, args []string) error {
			log := newLogger(stderr, level(f.debug, zerolog.WarnLevel))
			a, err := newApp(f, nil, log)
			if err != nil {
				return err
			}
			stdio := app.Stdio{In: os.Stdin, Out: stdout, Err: stderr}
			return a.Restic(cmd.Context(), stdio, args)
		},
	}
	// Everything after the first argument belongs to restic, also its flags.
	cmd.Flags().SetInterspersed(false)
	return cmd
}

// level returns debug when debug is set, else def.
func level(debug bool, def zerolog.Level) zerolog.Level {
	if debug {
		return zerolog.DebugLevel
	}
	return def
}

// newLogger writes human-readable logs to w, colored only when w is a terminal: under
// systemd, stderr goes to the journal, which would store the escape codes.
func newLogger(w io.Writer, level zerolog.Level) zerolog.Logger {
	out := zerolog.ConsoleWriter{Out: w, NoColor: !isTerminal(w)}
	return zerolog.New(out).Level(level).With().Timestamp().Logger()
}

// isTerminal reports whether w is a character device (a terminal).
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// newApp wires the application for the jobs (all when empty) with the process
// environment. Errors are logged here, one line per problem: an invalid config lists all
// of its problems at once.
func newApp(f *flags, jobs []string, log zerolog.Logger) (*app.App, error) {
	if f.config == "" {
		return nil, errNoConfig
	}
	a, err := app.New(app.Options{
		ConfigPath: f.config, Jobs: jobs, Getenv: os.Getenv, Environ: os.Environ(), Log: log,
	})
	if err != nil {
		for _, problem := range joined(err) {
			log.Error().Err(problem).Msg("startup failed")
		}
		if needsConfigHint(err, f) {
			log.Warn().Str("config", f.config).Msg("users other than root read this file " +
				"by default; pass --config <file>, or run restor as root to use " +
				app.SystemConfig)
		}
		return nil, errReported
	}
	return a, nil
}

// needsConfigHint reports whether err says that the default configuration file of a user
// other than root cannot be read: they may have meant the system one.
func needsConfigHint(err error, f *flags) bool {
	var pathErr *fs.PathError
	return f.config == f.defaultConfig && f.config != app.SystemConfig &&
		errors.As(err, &pathErr) && pathErr.Path == f.config &&
		(errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission))
}

// runBackup wires the application and runs one backup cycle of the jobs (all when
// empty). Errors are logged here.
func runBackup(ctx context.Context, f *flags, jobs []string, log zerolog.Logger) error {
	log.Info().Str("version", Version).Str("commit", Commit).Str("build_date", BuildDate).
		Str("go_version", GoVersion).Str("config", f.config).Msg("starting restor")
	a, err := newApp(f, jobs, log)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	if err := waitForRun(ctx, done, a.ShutdownTimeout(), log); err != nil {
		log.Error().Err(err).Msg("backup run failed")
		return errReported
	}
	log.Info().Msg("backup run completed")
	return nil
}

// joined returns the errors that err joins (errors.Join, also wrapped), flattened, or err
// itself.
func joined(err error) []error {
	var multi interface{ Unwrap() []error }
	if !errors.As(err, &multi) {
		return []error{err}
	}
	var all []error
	for _, e := range multi.Unwrap() {
		all = append(all, joined(e)...)
	}
	return all
}

// waitForRun waits for the run to finish; after ctx is cancelled it waits at most timeout
// (the time the running jobs need to restart their services), then gives up.
func waitForRun(ctx context.Context, done <-chan error, timeout time.Duration,
	log zerolog.Logger,
) error {
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	log.Warn().Stringer("timeout", timeout).
		Msg("shutdown requested, waiting for running jobs to restart their services " +
			"and run their post hooks")
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return errShutdownDeadline
	}
}
