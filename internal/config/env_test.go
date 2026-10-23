package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadEnvFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, content string
		want          map[string]string
		wantErr       bool
	}{
		{
			name: "values", content: "A=1\n# c\n\nB=\"two\"\n",
			want: map[string]string{"A": "1", "B": "two"},
		},
		{name: "line without =", content: "A=1\nbroken\n", wantErr: true},
		{name: "unterminated quote", content: "A=\"1\n", wantErr: true},
		{name: "line too long", content: string(make([]byte, 70000)), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := loadEnvFile(writeFile(t, t.TempDir(), "restic.env", tt.content))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
	_, err := loadEnvFile(t.TempDir() + "/missing.env")
	require.Error(t, err)
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
		`I=pa$word`,
		`J=${HOME}`,
	}, "\n")))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"A": "1", "B": `secret"`, "C": "x'", "D": "v", "E": "", "F": "pa$word",
		"G": "a # b", "H": "a#b", "I": "pa$word", "J": "${HOME}",
	}, got, "values are literal: no $ expansion")
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
