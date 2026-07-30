package utils

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/term"
)

var (
	askedPass      bool
	cachedPassword string
)

// GetPassword prompts for a password once per process and caches it for
// subsequent calls. Use ResetPassword when switching targets/credentials
// within the same process (e.g. from the TUI) so a stale password from a
// previous target isn't silently reused.
func GetPassword() (string, error) {
	if askedPass {
		return cachedPassword, nil
	}

	fmt.Print("Enter Password: ")

	// syscall.Stdin is the file descriptor for standard input
	// ReadPassword disables terminal echo automatically
	bytePassword, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return "", err
	}

	fmt.Println() // Print a newline because ReadPassword doesn't
	cachedPassword = string(bytePassword)
	askedPass = true
	return string(bytePassword), nil
}

// SetPassword pre-seeds the cached password so GetPassword() returns it
// without prompting. Used by callers (like the TUI) that collect a
// password through their own UI instead of a raw terminal prompt.
func SetPassword(password string) {
	cachedPassword = password
	askedPass = true
}

// ResetPassword clears the cached password so the next GetPassword() call
// prompts again (or the caller must SetPassword again). Call this whenever
// you're about to operate against a different target/credential set within
// the same process.
func ResetPassword() {
	cachedPassword = ""
	askedPass = false
}

func PrintHeader(header string) {
	fmt.Println("\n\n-----------------------------------------------------")
	fmt.Println(header)
	fmt.Println("-----------------------------------------------------")
}

func CheckCliCmdExist(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

// WithResultBanner prepends a clear pass/fail line to captured command
// output, so a TUI (or anything else consuming captured text) doesn't
// have to parse the output to know whether the underlying action
// actually succeeded - the detailed printed diagnostics remain intact
// below the banner either way.
func WithResultBanner(output string, runErr error) string {
	if runErr == nil {
		return "=== SUCCEEDED ===\n\n" + output
	}
	return fmt.Sprintf("=== FAILED: %v ===\n\n%s", runErr, output)
}

// Redact returns a fixed-width mask for a secret value, used any time a
// credential or secret value would otherwise be printed to the terminal.
// Callers that genuinely need the plaintext (e.g. an explicit --reveal
// flag) should bypass this and print the value directly.
func Redact(value string) string {
	if value == "" {
		return "<empty>"
	}
	return fmt.Sprintf("<redacted:%d bytes>", len(value))
}

// CaptureStdout runs fn with os.Stdout temporarily redirected into an
// in-memory buffer and returns everything fn printed. This lets the TUI
// reuse the existing CLI inventory functions (which print directly)
// without having to refactor every one of them into data-returning
// functions. Not safe to call concurrently from multiple goroutines since
// it swaps the process-wide os.Stdout.
func CaptureStdout(fn func()) (string, error) {
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	os.Stdout = w

	outCh := make(chan string)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, r)
		outCh <- buf.String()
	}()

	func() {
		defer func() {
			os.Stdout = origStdout
			w.Close()
		}()
		fn()
	}()

	result := <-outCh
	r.Close()
	return strings.TrimRight(result, "\n") + "\n", nil
}
