package parser

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InstructionType enumerates the six supported instructions.
type InstructionType string

const (
	FROM    InstructionType = "FROM"
	COPY    InstructionType = "COPY"
	RUN     InstructionType = "RUN"
	WORKDIR InstructionType = "WORKDIR"
	ENV     InstructionType = "ENV"
	CMD     InstructionType = "CMD"
)

// Instruction represents one parsed line of the Docksmithfile.
type Instruction struct {
	Type    InstructionType
	Raw     string // full original text of the instruction (excluding keyword)
	Line    int
	// Parsed fields
	FromImage string // FROM
	FromTag   string // FROM
	CopySrc   string // COPY
	CopyDest  string // COPY
	RunCmd    string // RUN
	WorkDir   string // WORKDIR
	EnvKey    string // ENV
	EnvValue  string // ENV
	CmdArgs   []string // CMD
}

// ParseFile reads and parses a Docksmithfile from the given context directory.
func ParseFile(contextDir string) ([]Instruction, error) {
	path := filepath.Join(contextDir, "Docksmithfile")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("Docksmithfile not found in %s", contextDir)
		}
		return nil, err
	}
	defer f.Close()

	var instructions []Instruction
	scanner := bufio.NewScanner(f)
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())

		// Skip blank lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Split keyword from rest
		parts := strings.SplitN(line, " ", 2)
		keyword := strings.ToUpper(parts[0])
		rest := ""
		if len(parts) == 2 {
			rest = strings.TrimSpace(parts[1])
		}

		instr := Instruction{Line: lineNo, Raw: rest}

		switch keyword {
		case "FROM":
			instr.Type = FROM
			if rest == "" {
				return nil, fmt.Errorf("line %d: FROM requires an argument", lineNo)
			}
			imgParts := strings.SplitN(rest, ":", 2)
			instr.FromImage = imgParts[0]
			instr.FromTag = "latest"
			if len(imgParts) == 2 {
				instr.FromTag = imgParts[1]
			}

		case "COPY":
			instr.Type = COPY
			copyParts := strings.Fields(rest)
			if len(copyParts) < 2 {
				return nil, fmt.Errorf("line %d: COPY requires <src> <dest>", lineNo)
			}
			instr.CopySrc = copyParts[0]
			instr.CopyDest = copyParts[1]

		case "RUN":
			instr.Type = RUN
			if rest == "" {
				return nil, fmt.Errorf("line %d: RUN requires a command", lineNo)
			}
			instr.RunCmd = rest

		case "WORKDIR":
			instr.Type = WORKDIR
			if rest == "" {
				return nil, fmt.Errorf("line %d: WORKDIR requires a path", lineNo)
			}
			instr.WorkDir = rest

		case "ENV":
			instr.Type = ENV
			// Support KEY=VALUE and KEY VALUE forms
			if strings.Contains(rest, "=") {
				idx := strings.Index(rest, "=")
				instr.EnvKey = strings.TrimSpace(rest[:idx])
				instr.EnvValue = rest[idx+1:]
			} else {
				envParts := strings.SplitN(rest, " ", 2)
				if len(envParts) < 2 {
					return nil, fmt.Errorf("line %d: ENV requires KEY=VALUE or KEY VALUE", lineNo)
				}
				instr.EnvKey = strings.TrimSpace(envParts[0])
				instr.EnvValue = strings.TrimSpace(envParts[1])
			}

		case "CMD":
			instr.Type = CMD
			// Must be JSON array form
			var cmdArgs []string
			if err := json.Unmarshal([]byte(rest), &cmdArgs); err != nil {
				return nil, fmt.Errorf("line %d: CMD requires JSON array form, e.g. [\"exec\",\"arg\"]: %w", lineNo, err)
			}
			instr.CmdArgs = cmdArgs

		default:
			return nil, fmt.Errorf("line %d: unrecognised instruction %q", lineNo, keyword)
		}

		instructions = append(instructions, instr)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return instructions, nil
}
