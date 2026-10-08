package config

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v4"
)

var (
	// ErrEnvFormat is returned for an env file that none of the supported formats can
	// parse.
	ErrEnvFormat = errors.New("unsupported env file format")

	errDotEnvLine  = errors.New(`expected KEY=value`)
	errDotEnvQuote = errors.New("unterminated quote")
)

// mergeEnvironments returns the env file's variables overridden by the inline ones. All
// values are expanded with getenv.
func mergeEnvironments(envFile string, inline map[string]string,
	getenv func(string) string,
) (map[string]string, error) {
	env := map[string]string{}
	if envFile != "" {
		fileEnv, err := loadEnvFile(envFile)
		if err != nil {
			return nil, fmt.Errorf("env file %s: %w", envFile, err)
		}
		maps.Copy(env, fileEnv)
	}
	maps.Copy(env, inline)
	for k, v := range env {
		env[k] = expand(v, getenv)
	}
	return env, nil
}

// expand replaces $VAR and ${VAR} with getenv(VAR); ${VAR:-default} uses default when
// VAR is unset or empty, and $$ is a literal $.
func expand(s string, getenv func(string) string) string {
	return os.Expand(s, func(name string) string {
		if name == "$" {
			return "$"
		}
		name, def, hasDefault := strings.Cut(name, ":-")
		if v := getenv(name); v != "" || !hasDefault {
			return v
		}
		return def
	})
}

// loadEnvFile parses a .env, .yaml/.yml or .json file; other extensions are tried in
// that order.
func loadEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	switch filepath.Ext(path) {
	case ".env":
		return parseDotEnv(data)
	case ".yaml", ".yml":
		return parseYAMLEnv(data)
	case ".json":
		return parseJSONEnv(data)
	}
	for _, parse := range []func([]byte) (map[string]string, error){
		parseDotEnv, parseYAMLEnv, parseJSONEnv,
	} {
		if env, err := parse(data); err == nil {
			return env, nil
		}
	}
	return nil, ErrEnvFormat
}

// parseDotEnv parses KEY=value lines. Blank lines and # comments are skipped, and an
// "export " prefix is ignored. A value in double quotes is taken as is; a value in single
// quotes is also kept literal by expand (its $ are escaped). An unquoted value ends at " #".
// A line without "=" is an error, so a mistyped secret is not silently dropped.
func parseDotEnv(data []byte) (map[string]string, error) {
	env := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, err := dotEnvLine(line)
		if err != nil {
			return nil, fmt.Errorf("parse .env: line %d: %w", n, err)
		}
		env[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse .env: %w", err)
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
	quote := value[0]
	end := strings.IndexByte(value[1:], quote)
	if end < 0 {
		return "", errDotEnvQuote
	}
	v := value[1 : end+1]
	if quote == '\'' {
		v = strings.ReplaceAll(v, "$", "$$")
	}
	return v, nil
}

func parseYAMLEnv(data []byte) (map[string]string, error) {
	env := map[string]string{}
	if err := yaml.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	return env, nil
}

func parseJSONEnv(data []byte) (map[string]string, error) {
	env := map[string]string{}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	return env, nil
}
