package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

var (
	errDotEnvLine  = errors.New(`expected KEY=value`)
	errDotEnvQuote = errors.New("unterminated quote")
)

// loadEnvFile reads a .env file (see parseDotEnv); the values are taken literally, a $
// in a password stays a $.
func loadEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseDotEnv(data)
}

// parseDotEnv parses KEY=value lines of any length. Blank lines and # comments are
// skipped, and an "export " prefix is ignored. A value in double or single quotes is
// taken as is, without escape sequences (unlike systemd's EnvironmentFile=); an unquoted
// value ends at " #". A line without "=" is an error, so a mistyped secret is not
// silently dropped.
func parseDotEnv(data []byte) (map[string]string, error) {
	env := map[string]string{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, err := dotEnvLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		env[key] = value
	}
	return env, nil
}

// dotEnvLine splits one non-comment .env line into key and unquoted value.
func dotEnvLine(line string) (key, value string, err error) {
	key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
	key = strings.TrimSpace(key)
	if !ok || key == "" || strings.ContainsAny(key, " \t") {
		return "", "", errDotEnvLine
	}
	value, err = dotEnvValue(strings.TrimSpace(value))
	return key, value, err
}

// dotEnvValue unquotes one .env value (see parseDotEnv).
func dotEnvValue(value string) (string, error) {
	if value == "" || (value[0] != '"' && value[0] != '\'') {
		v, _, _ := strings.Cut(value, " #")
		return strings.TrimSpace(v), nil
	}
	end := strings.IndexByte(value[1:], value[0])
	if end < 0 {
		return "", errDotEnvQuote
	}
	return value[1 : end+1], nil
}
