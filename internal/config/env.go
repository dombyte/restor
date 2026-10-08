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

// ErrEnvFormat is returned for an env file that none of the supported formats can parse.
var ErrEnvFormat = errors.New("unsupported env file format")

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
		env[k] = os.Expand(v, getenv)
	}
	return env, nil
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

// parseDotEnv parses KEY=value lines; blank lines and # comments are skipped, and one
// pair of surrounding quotes is removed from values.
func parseDotEnv(data []byte) (map[string]string, error) {
	env := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		env[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse .env: %w", err)
	}
	return env, nil
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
