package restic

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Restic wraps the restic CLI
type Restic struct {
	repo       string
	env        []string
	globalOpts []string
	logger     zerolog.Logger
}

// NewRestic creates a new Restic wrapper
func NewRestic(repo string, env []string, globalOpts []string, logger ...zerolog.Logger) *Restic {
	l := log.Logger
	if len(logger) > 0 {
		l = logger[0]
	}
	return &Restic{
		repo:       repo,
		env:        env,
		globalOpts: globalOpts,
		logger:     l,
	}
}

// buildCommand builds the base restic command
func (r *Restic) buildCommand(args ...string) *exec.Cmd {
	cmdArgs := []string{"-r", r.repo}
	cmdArgs = append(cmdArgs, r.globalOpts...)
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.Command("restic", cmdArgs...)
	cmd.Env = r.env

	return cmd
}

// Backup performs a backup of the specified sources with the given tag
func (r *Restic) Backup(ctx context.Context, sources []string, tag string, extraOpts []string) (string, error) {
	if len(sources) == 0 {
		return "", fmt.Errorf("no sources specified for backup")
	}

	cmd := r.buildCommand("backup")

	// Add tag if specified
	// Add extra options (e.g., --exclude patterns)
	cmd.Args = append(cmd.Args, extraOpts...)

	if tag != "" {
		cmd.Args = append(cmd.Args, "--tag", tag)
	}

	// Add sources
	cmd.Args = append(cmd.Args, sources...)

	r.logger.Info().Strs("sources", sources).Str("tag", tag).Msg("Starting backup")

	output, err := cmd.CombinedOutput()
	if err != nil {
		r.logger.Error().Err(err).Bytes("output", output).Strs("sources", sources).Msg("Backup failed")
		return "", fmt.Errorf("backup failed: %w", err)
	}

	// Extract snapshot ID from output
	snapshotID := extractSnapshotID(string(output))

	if snapshotID == "" {
		log.Warn().Msg("Could not extract snapshot ID from backup output")
		// This is not necessarily a critical error
		return string(output), nil
	}

	r.logger.Info().Str("snapshot_id", snapshotID).Msg("Backup completed successfully")
	return snapshotID, nil
}

// extractSnapshotID extracts the snapshot ID from restic backup output
func extractSnapshotID(output string) string {
	// Look for lines like: "snapshot <id> saved"
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "snapshot ") && strings.Contains(line, " saved") {
			parts := strings.Split(line, " ")
			if len(parts) >= 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

// Forget marks snapshots for removal based on retention policy and tag
// This does NOT prune - prune should be run separately for the entire repository
func (r *Restic) Forget(ctx context.Context, tag string, retentionPolicy string, extraOpts []string) error {
	cmd := r.buildCommand("forget")
	// Add extra options
	cmd.Args = append(cmd.Args, extraOpts...)

	// Add tag filter if specified
	if tag != "" {
		cmd.Args = append(cmd.Args, "--tag", tag)
	}

	// Add retention policy arguments
	if retentionPolicy != "" {
		policyParts := strings.Split(retentionPolicy, " ")
		cmd.Args = append(cmd.Args, policyParts...)
	}

	r.logger.Info().Str("tag", tag).Str("retention_policy", retentionPolicy).Msg("Running forget")

	output, err := cmd.CombinedOutput()
	if err != nil {
		r.logger.Error().Err(err).Bytes("output", output).Msg("Forget failed")
		return fmt.Errorf("forget failed: %w", err)
	}

	r.logger.Info().Msg("Forget completed successfully")
	return nil
}

// Prune removes unreferenced data from the repository
// This is repository-wide and should be run once after all forget operations
func (r *Restic) Prune(ctx context.Context) error {
	cmd := r.buildCommand("prune")

	r.logger.Info().Msg("Running prune for repository")

	output, err := cmd.CombinedOutput()
	if err != nil {
		r.logger.Error().Err(err).Bytes("output", output).Msg("Prune failed")
		return fmt.Errorf("prune failed: %w", err)
	}

	r.logger.Info().Msg("Prune completed successfully")
	return nil
}

// Unlock removes stale locks from the repository
func (r *Restic) Unlock(ctx context.Context) error {
	cmd := r.buildCommand("unlock")

	r.logger.Info().Msg("Running unlock for repository")

	output, err := cmd.CombinedOutput()
	if err != nil {
		r.logger.Error().Err(err).Bytes("output", output).Msg("Unlock failed")
		return fmt.Errorf("unlock failed: %w", err)
	}

	r.logger.Info().Msg("Unlock completed successfully")
	return nil
}
