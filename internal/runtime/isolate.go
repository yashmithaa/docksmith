package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// IsolateAndRun executes cmd inside rootDir using Linux namespaces for isolation.
func IsolateAndRun(rootDir string, cmd []string, env []string, workDir string) error {
	if len(cmd) == 0 {
		return fmt.Errorf("no command specified")
	}

	if workDir == "" {
		workDir = "/"
	}

	if os.Getenv("__DOCKSMITH_CHILD") == "1" {
		return runChild(rootDir, cmd, env, workDir)
	}

	return runParent(rootDir, cmd, env, workDir)
}

func runParent(rootDir string, cmd []string, env []string, workDir string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine executable path: %w", err)
	}

	args := append([]string{"__isolate", rootDir, workDir}, cmd...)
	child := exec.Command(self, args...)

	child.Env = append(env, "__DOCKSMITH_CHILD=1")
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr

	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUTS |
			syscall.CLONE_NEWPID |
			syscall.CLONE_NEWNS |
			syscall.CLONE_NEWIPC,
	}

	if err := child.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return &ExitError{Code: exitErr.ExitCode()}
		}
		return err
	}
	return nil
}

type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("container exited with code %d", e.Code)
}

func runChild(rootDir string, cmd []string, env []string, workDir string) error {
	// Make the mount namespace private so our mounts don't leak to host
	if err := syscall.Mount("", "/", "", syscall.MS_PRIVATE|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("remount private: %w", err)
	}

	// Bind mount rootDir onto itself to make it a mountpoint
	if err := syscall.Mount(rootDir, rootDir, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("bind mount rootDir: %w", err)
	}

	// Mount /proc inside the container
	procDir := filepath.Join(rootDir, "proc")
	if err := os.MkdirAll(procDir, 0555); err != nil {
		return fmt.Errorf("mkdir /proc: %w", err)
	}
	if err := syscall.Mount("proc", procDir, "proc", 0, ""); err != nil {
		_ = err // non-fatal
	}

	// Set hostname
	_ = syscall.Sethostname([]byte("docksmith"))

	// Pivot root
	if err := pivotRoot(rootDir); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}

	// Set working directory
	if err := os.Chdir(workDir); err != nil {
		_ = os.Chdir("/")
	}

	// Find binary
	binary, err := lookupInRoot(cmd[0])
	if err != nil {
		return fmt.Errorf("executable not found: %s", cmd[0])
	}

	// Filter sentinel env var
	filteredEnv := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "__DOCKSMITH_CHILD=") {
			filteredEnv = append(filteredEnv, e)
		}
	}

	return syscall.Exec(binary, cmd, filteredEnv)
}

func pivotRoot(newRoot string) error {
	// Create .pivot_old inside newRoot
	pivotOld := filepath.Join(newRoot, ".pivot_old")
	if err := os.MkdirAll(pivotOld, 0700); err != nil {
		return fmt.Errorf("mkdir pivot_old: %w", err)
	}

	// pivot_root
	if err := syscall.PivotRoot(newRoot, pivotOld); err != nil {
		return fmt.Errorf("syscall pivot_root: %w", err)
	}

	// chdir to new root
	if err := os.Chdir("/"); err != nil {
		return err
	}

	// Unmount old root
	if err := syscall.Unmount("/.pivot_old", syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("unmount old root: %w", err)
	}

	return os.Remove("/.pivot_old")
}

func lookupInRoot(binary string) (string, error) {
	if filepath.IsAbs(binary) {
		if _, err := os.Stat(binary); err == nil {
			return binary, nil
		}
		return "", fmt.Errorf("not found: %s", binary)
	}

	searchPaths := []string{
		"/usr/local/sbin",
		"/usr/local/bin",
		"/usr/sbin",
		"/usr/bin",
		"/sbin",
		"/bin",
	}

	for _, dir := range searchPaths {
		candidate := filepath.Join(dir, binary)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH", binary)
}

func DispatchIsolateChild(args []string) {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "internal error: __isolate requires rootDir workDir cmd...")
		os.Exit(1)
	}
	rootDir := args[0]
	workDir := args[1]
	cmd := args[2:]

	env := os.Environ()

	if err := runChild(rootDir, cmd, env, workDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
