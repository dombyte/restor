// Package main is the restor CLI. `restor --config <file>` runs one backup cycle and exits
// 0 when every project was backed up, 1 otherwise; `restor version` (or --version) prints
// the build information.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/dombyte/restor/internal/app"
)

// shutdownTimeout bounds the run after SIGINT/SIGTERM: running projects restart their
// services, then the process exits 1 regardless.
const shutdownTimeout = 5 * time.Minute

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
	// errMissingConfig is returned when --config is not set.
	errMissingConfig = errors.New(`required flag "config" not set`)
	// errShutdownDeadline is returned when the run outlives shutdownTimeout after a signal.
	errShutdownDeadline = errors.New("shutdown deadline exceeded")
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the CLI with args and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	root := newRootCommand(stdout, stderr)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		if !errors.Is(err, errReported) {
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

func newRootCommand(stdout, stderr io.Writer) *cobra.Command {
	var configPath string
	var debug bool
	root := &cobra.Command{
		Use:   "restor --config <file>",
		Short: "Restic backup orchestrator",
		Long: `restor orchestrates restic backups of several projects on one host.

It stops the services of each project (Docker/Podman Compose, systemd), backs up
its sources, starts the services again and applies the retention policies.`,
		Version:       Version,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if configPath == "" {
				return errMissingConfig
			}
			return runBackup(cmd.Context(), configPath, newLogger(stderr, debug))
		},
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetVersionTemplate(buildInfo() + "\n")
	root.Flags().StringVar(&configPath, "config", "", "configuration file (required)")
	root.Flags().BoolVar(&debug, "debug", false, "enable debug logging")
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the build information",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { cmd.Println(buildInfo()) },
	})
	return root
}

// newLogger writes human-readable logs to w, colored only when w is a terminal: under
// systemd, stderr goes to the journal, which would store the escape codes.
func newLogger(w io.Writer, debug bool) zerolog.Logger {
	level := zerolog.InfoLevel
	if debug {
		level = zerolog.DebugLevel
	}
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

// runBackup wires the application and runs one backup cycle. Errors are logged here.
func runBackup(ctx context.Context, configPath string, log zerolog.Logger) error {
	log.Info().Str("version", Version).Str("commit", Commit).Str("build_date", BuildDate).
		Str("go_version", GoVersion).Str("config", configPath).Msg("starting restor")
	a, err := app.New(app.Options{
		ConfigPath: configPath, Getenv: os.Getenv, Environ: os.Environ(), Log: log,
	})
	if err != nil {
		log.Error().Err(err).Msg("startup failed")
		return errReported
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	if err := waitForRun(ctx, done, log); err != nil {
		log.Error().Err(err).Msg("backup run failed")
		return errReported
	}
	log.Info().Msg("backup run completed")
	return nil
}

// waitForRun waits for the run to finish; after ctx is cancelled it waits at most
// shutdownTimeout.
func waitForRun(ctx context.Context, done <-chan error, log zerolog.Logger) error {
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	log.Warn().Dur("timeout", shutdownTimeout).
		Msg("shutdown requested, waiting for running projects to restart their services")
	select {
	case err := <-done:
		return err
	case <-time.After(shutdownTimeout):
		return errShutdownDeadline
	}
}
