package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dombyte/dbk/backup"
	"github.com/dombyte/dbk/config"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// Version is set at build time using -ldflags
var Version = "dev"

// BuildDate is set at build time using -ldflags
var BuildDate = ""

// GitCommit is set at build time using -ldflags
var GitCommit = ""

var (
	configFlag string
	debugFlag  bool
	versionFlag bool
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "dbk --config <file>",
	Short: "Docker container backup tool using restic",
	Long: `dbk is a CLI tool for backing up Docker containers using restic.

It supports multiple projects, parallel/sequential execution, and automatic
container stop/start during backup.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBackup(cmd.Context())
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	cobra.CheckErr(rootCmd.Execute())
}

// versionCmd represents the version command
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number of dbk",
	Long:  "All software has versions. This is dbk's",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("dbk version %s\n", Version)
		if BuildDate != "" {
			fmt.Printf("build date: %s\n", BuildDate)
		}
		if GitCommit != "" {
			fmt.Printf("git commit: %s\n", GitCommit)
		}
	},
}

func init() {
	cobra.OnInitialize(initConfig)

	// Add version flag
	rootCmd.Flags().BoolVarP(&versionFlag, "version", "v", false, "Print version and exit")

	// Add subcommands
	rootCmd.AddCommand(versionCmd)

	rootCmd.Flags().StringVar(&configFlag, "config", "", "Configuration file path (required)")
	rootCmd.Flags().BoolVar(&debugFlag, "debug", false, "Enable debug logging")

	// Set up pre-run to handle version flag before required flag check
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if versionFlag {
			fmt.Printf("dbk version: %s\n", Version)
			if BuildDate != "" {
				fmt.Printf("build date: %s\n", BuildDate)
			}
			if GitCommit != "" {
				fmt.Printf("git commit: %s\n", GitCommit)
			}
			os.Exit(0)
		}
	}

	rootCmd.MarkFlagRequired("config")
}

// initConfig initializes the configuration
func initConfig() {
	// Set up logging
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix

	// Always use console writer for human-readable colored output
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	if debugFlag {
		log.Logger = log.Level(zerolog.DebugLevel)
	} else {
		log.Logger = log.Level(zerolog.InfoLevel)
	}
}

// runBackup executes the backup process
func runBackup(ctx context.Context) error {
	// Set up signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Info().Msg("Received shutdown signal, canceling backup...")
		cancel()
	}()

	// Load configuration
	log.Info().Str("config_file", configFlag).Msg("Loading configuration")

	cfg, err := config.LoadConfig(configFlag)
	if err != nil {
		log.Error().Err(err).Msg("Failed to load configuration")
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Convert config to projects
	projects, err := cfg.ToProjects(cfg.Environments)
	if err != nil {
		log.Error().Err(err).Msg("Failed to resolve projects")
		return fmt.Errorf("failed to resolve projects: %w", err)
	}

	if len(projects) == 0 {
		log.Warn().Msg("No projects found in configuration")
		return nil
	}

	log.Info().
		Str("mode", cfg.GetMode()).
		Int("project_count", len(projects)).
		Msg("Loaded configuration")

	// List projects
	for _, project := range projects {
		log.Info().
			Str("project", project.Name).
			Str("service_manager", project.ServiceManager).
			Str("compose_file", project.ComposeFile).
			Strs("systemd_units", project.SystemdUnits).
			Str("systemd_scope", project.SystemdScope).
			Strs("sources", project.Sources).
			Strs("services", project.Services).
			Bool("auto_prune", project.AutoPrune).
			Msg("Project configuration")
	}

	// Create and run manager
	manager := backup.NewManager(cfg, projects)

	if err := manager.Run(ctx); err != nil {
		log.Error().Err(err).Msg("Backup manager failed")
		return fmt.Errorf("backup failed: %w", err)
	}

	log.Info().Msg("All backups completed successfully")
	return nil
}
