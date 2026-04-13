package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"docksmith/internal/builder"
	"docksmith/internal/image"
	"docksmith/internal/runtime"
)

// Execute is the main entrypoint for the CLI.
func Execute() error {
	args := os.Args[1:]
	if len(args) == 0 {
		printUsage()
		return nil
	}

	switch args[0] {
	case "build":
		return runBuild(args[1:])
	case "images":
		return runImages()
	case "rmi":
		return runRmi(args[1:])
	case "run":
		return runRun(args[1:])
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func printUsage() {
	fmt.Println(`Docksmith - A simplified Docker-like build and runtime system

Usage:
  docksmith build -t <name:tag> [--no-cache] <context>
  docksmith images
  docksmith rmi <name:tag>
  docksmith run [-e KEY=VALUE] <name:tag> [cmd...]`)
}

// runBuild handles: docksmith build -t <name:tag> [--no-cache] <context>
func runBuild(args []string) error {
	var tag string
	var noCache bool
	var context string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-t":
			if i+1 >= len(args) {
				return errors.New("flag -t requires an argument")
			}
			i++
			tag = args[i]
		case "--no-cache":
			noCache = true
		default:
			if context != "" {
				return fmt.Errorf("unexpected argument: %s", args[i])
			}
			context = args[i]
		}
	}

	if tag == "" {
		return errors.New("flag -t is required")
	}
	if context == "" {
		return errors.New("build context directory is required")
	}

	parts := strings.SplitN(tag, ":", 2)
	name := parts[0]
	t := "latest"
	if len(parts) == 2 {
		t = parts[1]
	}

	return builder.Build(name, t, context, noCache)
}

// runImages handles: docksmith images
func runImages() error {
	return image.List()
}

// runRmi handles: docksmith rmi <name:tag>
func runRmi(args []string) error {
	if len(args) == 0 {
		return errors.New("rmi requires a <name:tag> argument")
	}
	parts := strings.SplitN(args[0], ":", 2)
	name := parts[0]
	tag := "latest"
	if len(parts) == 2 {
		tag = parts[1]
	}
	return image.Remove(name, tag)
}

// runRun handles: docksmith run [-e KEY=VALUE] <name:tag> [cmd...]
func runRun(args []string) error {
	var envOverrides []string
	var nameTag string
	var cmdArgs []string

	for i := 0; i < len(args); i++ {
		if args[i] == "-e" {
			if i+1 >= len(args) {
				return errors.New("flag -e requires an argument")
			}
			i++
			envOverrides = append(envOverrides, args[i])
		} else if nameTag == "" {
			nameTag = args[i]
		} else {
			cmdArgs = append(cmdArgs, args[i:]...)
			break
		}
	}

	if nameTag == "" {
		return errors.New("run requires a <name:tag> argument")
	}

	parts := strings.SplitN(nameTag, ":", 2)
	name := parts[0]
	tag := "latest"
	if len(parts) == 2 {
		tag = parts[1]
	}

	return runtime.Run(name, tag, cmdArgs, envOverrides)
}
