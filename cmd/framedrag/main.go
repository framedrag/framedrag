// framedrag: curated IP reputation feeds, dragged into the null route.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// Set by the linker at build time (see Makefile LDFLAGS).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Exit codes. Anything unexpected maps to exitHardError.
const (
	exitOK        = 0
	exitHardError = 1 // invalid config, target apply failed, I/O errors
	exitUnhealthy = 2 // at least one feed SUSPECT, STALE, or FAILED
	exitDiff      = 3 // catalog sync found drift vs upstream
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the CLI and returns the process exit code. It exists so
// tests can drive the whole binary in-process.
func run(args []string, stdout, stderr io.Writer) int {
	a := &app{stdout: stdout, stderr: stderr}
	root := newRoot(a)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	err := root.Execute()
	if err == nil {
		return exitOK
	}
	var ee exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			fmt.Fprintln(stderr, "framedrag:", ee.msg)
		}
		return ee.code
	}
	fmt.Fprintln(stderr, "framedrag:", err)
	return exitHardError
}

// exitError carries a specific exit code out of a command. An empty
// msg means the command already printed its output and only the code
// matters (e.g. exit 2 after the health table).
type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }
