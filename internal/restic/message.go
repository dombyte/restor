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
// {"message_type":"exit_error","code":1,"message":"Fatal: …"}. The runner joins the last
// output lines with "; ", so each part holds at most one message; a part that is no
// complete message stays as it is. err itself is returned when nothing changed.
func readable(err error) error {
	parts := strings.Split(err.Error(), "; ")
	changed := false
	for i, part := range parts {
		start := strings.Index(part, jsonStart)
		if start < 0 {
			continue
		}
		if text, ok := messageText(part[start:]); ok {
			parts[i], changed = part[:start]+text, true
		}
	}
	if !changed {
		return err
	}
	return &readableError{msg: strings.Join(parts, "; "), err: err}
}

// message is an error message of `restic backup --json`: "exit_error" carries its text
// in message, "error" (a file that could not be read) in error.message.
type message struct {
	Message string `json:"message"`
	Error   struct {
		Message string `json:"message"`
	} `json:"error"`
}

// messageText returns the text of the JSON message s.
func messageText(s string) (string, bool) {
	var m message
	if json.Unmarshal([]byte(s), &m) != nil {
		return "", false
	}
	switch {
	case m.Message != "":
		return m.Message, true
	case m.Error.Message != "":
		return m.Error.Message, true
	default:
		return "", false
	}
}
