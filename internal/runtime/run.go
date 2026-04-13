package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"docksmith/internal/image"
)

// Run assembles the filesystem for name:tag and runs the container.
func Run(name, tag string, cmdArgs []string, envOverrides []string) error {
	m, err := image.Load(name, tag)
	if err != nil {
		return err
	}

	// Determine command
	finalCmd := cmdArgs
	if len(finalCmd) == 0 {
		finalCmd = m.Config.Cmd
	}
	if len(finalCmd) == 0 {
		return fmt.Errorf("no command specified and image has no CMD defined")
	}

	// Build environment: image ENV first, then overrides
	env := buildEnv(m.Config.Env, envOverrides)

	// Assemble filesystem in a temp directory
	tmpDir, err := os.MkdirTemp("", "docksmith-run-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := image.AssembleFilesystem(m.Layers, tmpDir); err != nil {
		return fmt.Errorf("assembling filesystem: %w", err)
	}

	workDir := m.Config.WorkingDir
	if workDir == "" {
		workDir = "/"
	}

	// Ensure workdir exists in the assembled filesystem
	hostWorkDir := filepath.Join(tmpDir, workDir)
	if err := os.MkdirAll(hostWorkDir, 0755); err != nil {
		return fmt.Errorf("creating working directory: %w", err)
	}

	if err := IsolateAndRun(tmpDir, finalCmd, env, workDir); err != nil {
		if exitErr, ok := err.(*ExitError); ok {
			fmt.Fprintf(os.Stderr, "Container exited with code %d\n", exitErr.Code)
			os.Exit(exitErr.Code)
		}
		return err
	}
	return nil
}

// buildEnv merges image env with overrides. Overrides take precedence.
func buildEnv(imageEnv []string, overrides []string) []string {
	m := make(map[string]string)
	for _, e := range imageEnv {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		}
	}
	for _, e := range overrides {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		}
	}

	result := make([]string, 0, len(m))
	for k, v := range m {
		result = append(result, k+"="+v)
	}
	return result
}
