package command

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The test binary re-executes itself as the "external program" (helper process pattern),
// so the tests need no real tools.
const helperEnv = "RESTOR_COMMAND_HELPER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		os.Exit(helper(mode))
	}
	os.Exit(runTests(m))
}

// runTests runs the tests with GOCOVERDIR set for the helper processes: a coverage build
// of the helper warns on stderr when it is unset, which would change the output the
// tests check.
func runTests(m *testing.M) int {
	if os.Getenv("GOCOVERDIR") != "" {
		return m.Run()
	}
	dir, err := os.MkdirTemp("", "restor-cover-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	if err := os.Setenv("GOCOVERDIR", dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}

func helper(mode string) int {
	helpers := map[string]func() int{
		"echo":       helperEcho,
		"warn":       helperWarn,
		"stderrfail": helperStderrFail,
		"fail":       helperFail,
		"big":        helperBig,
		"term":       helperTerm,
		"group":      helperGroup,
		"termfile":   helperTermFile,
		"background": helperBackground,
		"sleep":      helperSleep,
	}
	if h, ok := helpers[mode]; ok {
		return h()
	}
	return 2
}

func helperEcho() int {
	fmt.Println(strings.Join(os.Args[1:], " "), os.Getenv("RESTOR_TEST_VALUE"))
	return 0
}

// helperWarn writes data to stdout and a warning to stderr, like podman compose.
func helperWarn() int {
	fmt.Fprintln(os.Stderr, ">>>> Executing external compose provider <<<<")
	fmt.Println(`{"Service":"web"}`)
	return 0
}

// helperStderrFail reports progress on stdout and its failure on stderr, like restic.
func helperStderrFail() int {
	fmt.Println(`{"message_type":"status"}`)
	fmt.Fprintln(os.Stderr, "Fatal: boom")
	return 1
}

// helperFail reports its failure on stdout only, like many scripts.
func helperFail() int {
	fmt.Println("line one")
	fmt.Println("line two")
	return 3
}

// helperBig prints three times maxOutput, then a snapshot line.
func helperBig() int {
	line := strings.Repeat("x", 1023) + "\n"
	for range 3 * maxOutput / len(line) {
		fmt.Print(line)
	}
	fmt.Println("snapshot 1a2b3c4d saved")
	return 0
}

// helperTerm reports SIGTERM, then exits: the runner must ask before it kills.
func helperTerm() int {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	if err := os.WriteFile(os.Getenv("RESTOR_READY_FILE"), nil, 0o600); err != nil {
		return 2
	}
	<-sig
	fmt.Println("got SIGTERM")
	return 0
}

// helperTermFile is like helperTerm, but reports SIGTERM in a file: its parent owns the
// output.
func helperTermFile() int {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	if err := os.WriteFile(os.Getenv("RESTOR_READY_FILE"), nil, 0o600); err != nil {
		return 2
	}
	<-sig
	if err := os.WriteFile(os.Getenv("RESTOR_TERM_FILE"), nil, 0o600); err != nil {
		return 2
	}
	return 0
}

// helperGroup starts a termfile child (like a hook script starting a program) and, on
// SIGTERM, waits for it: the child only ends if the runner signals the whole group.
func helperGroup() int {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	child := exec.Command(os.Args[0])
	child.Env = append(os.Environ(), helperEnv+"=termfile")
	if err := child.Start(); err != nil {
		return 2
	}
	<-sig
	if err := child.Wait(); err != nil {
		return 2
	}
	return 0
}

// helperBackground starts a sleep child that inherits its stdout, like a hook script
// that runs "daemon &", and exits successfully.
func helperBackground() int {
	child := exec.Command(os.Args[0])
	child.Env = append(os.Environ(), helperEnv+"=sleep")
	child.Stdout = os.Stdout
	if err := child.Start(); err != nil {
		return 2
	}
	fmt.Println("started")
	return 0
}

// helperSleep keeps running for a while, holding the output it inherited.
func helperSleep() int {
	time.Sleep(5 * time.Second)
	return 0
}

func newRunner(mode string, extra ...string) *Runner {
	env := append([]string{helperEnv + "=" + mode, "GOCOVERDIR=" + os.Getenv("GOCOVERDIR")},
		extra...)
	return New(env, zerolog.Nop())
}

func TestRunner_Run(t *testing.T) {
	t.Parallel()
	r := newRunner("echo", "RESTOR_TEST_VALUE=a", "RESTOR_TEST_VALUE=b")

	out, err := r.Run(context.Background(), os.Args[0], "x", "y")

	require.NoError(t, err)
	// A coverage build of the helper may append a GOCOVERDIR warning.
	assert.True(t, strings.HasPrefix(string(out), "x y b\n"),
		"args passed, later env entries win: %q", out)
}

func TestRunner_RunEnv(t *testing.T) {
	t.Parallel()
	r := newRunner("echo", "RESTOR_TEST_VALUE=a")

	out, err := r.RunEnv(context.Background(), []string{"RESTOR_TEST_VALUE=c"}, os.Args[0], "x")

	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(out), "x c\n"), "extra env wins: %q", out)
}

func TestRunner_Attach(t *testing.T) {
	t.Parallel()
	var out, errOut strings.Builder
	stdio := Stdio{In: strings.NewReader(""), Out: &out, Err: &errOut}

	err := newRunner("echo", "RESTOR_TEST_VALUE=a").
		Attach(context.Background(), stdio, os.Args[0], "x")

	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out.String(), "x a\n"), out.String())
}

func TestRunner_AttachFailure(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	stdio := Stdio{In: strings.NewReader(""), Out: &out, Err: &out}

	err := newRunner("fail").Attach(context.Background(), stdio, os.Args[0])

	var exit interface{ ExitCode() int }
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 3, exit.ExitCode())
	assert.Contains(t, out.String(), "line two", "output goes to the caller's streams")
	assert.ErrorIs(t, newRunner("echo").Attach(context.Background(), stdio, ""),
		ErrEmptyCommand)
}

func TestRunner_RunFailure(t *testing.T) {
	t.Parallel()
	r := newRunner("fail")

	out, err := r.Run(context.Background(), os.Args[0])

	require.Error(t, err)
	assert.Contains(t, string(out), "line two")
	var cmdErr *Error
	require.ErrorAs(t, err, &cmdErr)
	assert.True(t, strings.HasPrefix(cmdErr.Output, "line one; line two"), cmdErr.Output)
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 3, exitErr.ExitCode())
}

func TestRunner_RunReturnsStdoutOnly(t *testing.T) {
	t.Parallel()
	out, err := newRunner("warn").Run(context.Background(), os.Args[0])

	require.NoError(t, err)
	assert.NotContains(t, string(out), "compose provider", "stderr stays out of the output")
	assert.True(t, strings.HasPrefix(string(out), `{"Service":"web"}`+"\n"), out)
}

func TestRunner_RunFailureCarriesStderr(t *testing.T) {
	t.Parallel()
	out, err := newRunner("stderrfail").Run(context.Background(), os.Args[0])

	var cmdErr *Error
	require.ErrorAs(t, err, &cmdErr)
	assert.True(t, strings.HasPrefix(cmdErr.Output, "Fatal: boom"), cmdErr.Output)
	assert.NotContains(t, cmdErr.Output, "status", "stdout is left out when stderr has lines")
	assert.Contains(t, string(out), "status")
}

func TestRunner_RunKeepsTheLastOutput(t *testing.T) {
	t.Parallel()
	out, err := newRunner("big").Run(context.Background(), os.Args[0])

	require.NoError(t, err)
	assert.LessOrEqual(t, len(out), maxOutput)
	assert.Contains(t, string(out), "snapshot 1a2b3c4d saved\n")
}

func TestTailBuffer(t *testing.T) {
	t.Parallel()
	b := &tailBuffer{max: 4}
	for _, w := range []string{"ab", "cd", "ef", "g", "hijkl"} {
		n, err := b.Write([]byte(w))
		require.NoError(t, err)
		assert.Equal(t, len(w), n)
	}
	assert.Equal(t, "ijkl", string(b.Bytes()))
	assert.Equal(t, "ab", string((&tailBuffer{max: 4, buf: []byte("ab")}).Bytes()))
}

func TestRunner_RunCancelSendsSIGTERM(t *testing.T) {
	t.Parallel()
	ready := filepath.Join(t.TempDir(), "ready")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// The helper is a real process; wait until its signal handler is installed.
		for {
			if _, err := os.Stat(ready); err == nil {
				cancel()
				return
			}
			runtime.Gosched()
		}
	}()

	out, err := newRunner("term", "RESTOR_READY_FILE="+ready).Run(ctx, os.Args[0])

	require.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, string(out), "got SIGTERM")
}

func TestRunner_RunCancelSignalsTheProcessGroup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ready, term := filepath.Join(dir, "ready"), filepath.Join(dir, "term")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			if _, err := os.Stat(ready); err == nil {
				cancel()
				return
			}
			runtime.Gosched()
		}
	}()

	_, err := newRunner("group", "RESTOR_READY_FILE="+ready, "RESTOR_TERM_FILE="+term).
		Run(ctx, os.Args[0])

	require.ErrorIs(t, err, context.Canceled)
	assert.FileExists(t, term, "the child of the program got SIGTERM too")
}

func TestRunner_RunBackgroundProcessHoldsOutput(t *testing.T) {
	t.Parallel()
	r := newRunner("background")
	r.waitDelay = 100 * time.Millisecond

	out, err := r.Run(context.Background(), os.Args[0])

	require.NoError(t, err, "the program itself succeeded")
	assert.Equal(t, "started\n", string(out))
}

func TestSignalGroup_ExitedGroup(t *testing.T) {
	t.Parallel()
	cmd := exec.Command(os.Args[0])
	cmd.Env = []string{helperEnv + "=echo"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Run())

	require.NoError(t, signalGroup(cmd.Process.Pid, syscall.SIGKILL), "ESRCH is no error")
}

func TestRunner_RunEmpty(t *testing.T) {
	t.Parallel()
	_, err := New(nil, zerolog.Nop()).Run(context.Background(), "")
	require.ErrorIs(t, err, ErrEmptyCommand)
}

func TestRunner_RunCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newRunner("echo").Run(ctx, os.Args[0])

	require.ErrorIs(t, err, context.Canceled)
}

func TestError_Error(t *testing.T) {
	t.Parallel()
	base := assert.AnError
	assert.Equal(t, "command: restic: "+base.Error(), (&Error{Program: "restic", Err: base}).Error())
	assert.Equal(t, "command: restic: "+base.Error()+": boom",
		(&Error{Program: "restic", Output: "boom", Err: base}).Error())
}

func TestTail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{name: "empty", in: "", n: 3, want: ""},
		{name: "skips blank lines", in: "a\n\n b \n", n: 3, want: "a; b"},
		{name: "keeps the last n", in: "a\nb\nc\nd", n: 2, want: "c; d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tail([]byte(tt.in), tt.n))
		})
	}
}
