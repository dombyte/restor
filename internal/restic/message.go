package restic

import (
	"encoding/json"
	"strings"
)

// jsonStart starts every message of restic's --json output.
const jsonStart = `{"message_type"`

// readableError is a runner error whose message has restic's JSON messages replaced by
// their text; errors.Is and errors.As still reach the runner's error.
type readableError struct {
	msg string
	err error
}

func (e *readableError) Error() string { return e.msg }

// Unwrap returns the runner's error.
func (e *readableError) Unwrap() error { return e.err }

// readable returns err with each JSON message in its text replaced by what the message
// says, e.g. "Fatal: all source directories/files do not exist" instead of
// {"message_type":"exit_error","code":1,"message":"Fatal: …"}. A message is decoded from
// where it starts to where its JSON value ends, so a "; " inside it (the runner joins
// the output lines with "; ") does not split it; one that is not complete stays as it
// is. err itself is returned when nothing changed.
func readable(err error) error {
	rest := err.Error()
	var b strings.Builder
	changed := false
	for {
		start := strings.Index(rest, jsonStart)
		if start < 0 {
			break
		}
		text, n, ok := messageText(rest[start:])
		if !ok {
			n, text = len(jsonStart), jsonStart
		}
		b.WriteString(rest[:start] + text)
		rest, changed = rest[start+n:], changed || ok
	}
	if !changed {
		return err
	}
	b.WriteString(rest)
	return &readableError{msg: b.String(), err: err}
}

// message is an error message of `restic backup --json`: "exit_error" carries its text
// in message, "error" (a file that could not be read) in error.message.
type message struct {
	Message string `json:"message"`
	Error   struct {
		Message string `json:"message"`
	} `json:"error"`
}

// messageText returns the text of the JSON message at the start of s and its length.
func messageText(s string) (string, int, bool) {
	var m message
	dec := json.NewDecoder(strings.NewReader(s))
	if dec.Decode(&m) != nil {
		return "", 0, false
	}
	n := int(dec.InputOffset())
	switch {
	case m.Message != "":
		return m.Message, n, true
	case m.Error.Message != "":
		return m.Error.Message, n, true
	default:
		return "", 0, false
	}
}
