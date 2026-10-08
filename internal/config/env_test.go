package config

import (
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
		{name: "dotenv", file: "x.env", content: "A=1\n# c\n\nB=\"two\"\nbroken\n", want: want},
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

func TestMergeEnvironments(t *testing.T) {
	t.Parallel()
	path := writeFile(t, t.TempDir(), "x.env", "A=${X}\nB=file\n")
	got, err := mergeEnvironments(path, map[string]string{"B": "inline-${X}"},
		getenv(map[string]string{"X": "x"}))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"A": "x", "B": "inline-x"}, got)
}
