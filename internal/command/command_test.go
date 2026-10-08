package command

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

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
	os.Exit(m.Run())
}

func helper(mode string) int {
	switch mode {
	case "echo":
		fmt.Println(strings.Join(os.Args[1:], " "), os.Getenv("RESTOR_TEST_VALUE"))
		return 0
	case "fail":
		fmt.Println("line one")
		fmt.Println("line two")
		return 3
	default:
		return 2
	}
}

func newRunner(mode string, extra ...string) *Runner {
	env := append([]string{helperEnv + "=" + mode}, extra...)
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
