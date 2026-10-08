package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadEnvFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	want := map[string]string{"A": "1", "B": "two"}
	tests := []struct {
		name, file, content string
		want                map[string]string
		wantErr             bool
	}{
		{name: "dotenv", file: "x.env", content: "A=1\n# c\n\nB=\"two\"\n", want: want},
		{name: "dotenv without =", file: "x.env", content: "A=1\nbroken\n", wantErr: true},
		{name: "dotenv unterminated quote", file: "x.env", content: "A=\"1\n", wantErr: true},
		{name: "unknown extension as yaml", file: "env", content: "A: \"1\"\nB: two\n", want: want},
		{name: "unknown extension as json", file: "env", content: `{"A":"1","B":"two"}`, want: want},
		{name: "yaml", file: "x.yaml", content: "A: \"1\"\nB: two\n", want: want},
		{name: "yml", file: "x.yml", content: "A: \"1\"\nB: two\n", want: want},
		{name: "json", file: "x.json", content: `{"A":"1","B":"two"}`, want: want},
		{name: "unknown extension as dotenv", file: "env", content: "A=1\nB=two\n", want: want},
		{name: "broken yaml", file: "x.yaml", content: "A: [", wantErr: true},
		{name: "broken json", file: "x.json", content: "{", wantErr: true},
		{
			name: "dotenv line too long", file: "x.env", content: string(make([]byte, 70000)),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := loadEnvFile(writeFile(t, t.TempDir(), tt.file, tt.content))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
	_, err := loadEnvFile(dir + "/missing.env")
	require.Error(t, err)
}

func TestLoadEnvFile_UnknownFormat(t *testing.T) {
	t.Parallel()
	// Too long for the .env scanner, not YAML or JSON either.
	content := "{" + string(make([]byte, 70000))
	_, err := loadEnvFile(writeFile(t, t.TempDir(), "env", content))
	require.ErrorIs(t, err, ErrEnvFormat)
}

func TestParseDotEnv_Values(t *testing.T) {
	t.Parallel()
	got, err := parseDotEnv([]byte(strings.Join([]string{
		`export A=1`,
		`B=secret"`,
		`C="x'"`,
		`D=v # comment`,
		`E=""  # empty`,
		`F='pa$word'`,
		`G="a # b"`,
		`H=a#b`,
	}, "\n")))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"A": "1", "B": `secret"`, "C": "x'", "D": "v", "E": "", "F": "pa$$word",
		"G": "a # b", "H": "a#b",
	}, got)
}

func TestMergeEnvironments_LiteralDollar(t *testing.T) {
	t.Parallel()
	path := writeFile(t, t.TempDir(), "x.env", "A='pa$word'\nB=pa$$word\n")
	got, err := mergeEnvironments(path, map[string]string{"C": "x$$y"}, getenv(nil))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"A": "pa$word", "B": "pa$word", "C": "x$y"}, got)
}

func TestMergeEnvironments(t *testing.T) {
	t.Parallel()
	path := writeFile(t, t.TempDir(), "x.env", "A=${X}\nB=file\n")
	got, err := mergeEnvironments(path, map[string]string{"B": "inline-${X}"},
		getenv(map[string]string{"X": "x"}))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"A": "x", "B": "inline-x"}, got)
}

func TestExpand(t *testing.T) {
	t.Parallel()
	env := getenv(map[string]string{"SET": "value", "EMPTY": ""})
	tests := map[string]string{
		"${SET}":                   "value",
		"$SET/x":                   "value/x",
		"${MISSING}":               "",
		"${MISSING:-eu-central-1}": "eu-central-1",
		"${EMPTY:-fallback}":       "fallback",
		"${SET:-fallback}":         "value",
		"${MISSING:-}":             "",
		"a ${SET} b ${NOPE:-c}":    "a value b c",
		"pa$$word":                 "pa$word",
		"$$SET":                    "$SET",
	}
	for in, want := range tests {
		assert.Equal(t, want, expand(in, env), in)
	}
}
