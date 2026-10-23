package restic

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVersion(t *testing.T) {
	t.Parallel()
	v, err := ParseVersion("restic 0.19.1 compiled with go1.26.5 on linux/amd64\n")
	require.NoError(t, err)
	assert.Equal(t, "0.19.1", v.String())

	_, err = ParseVersion("restic dev")
	require.ErrorIs(t, err, ErrVersion)
}

func TestVersion_AtLeast(t *testing.T) {
	t.Parallel()
	v := Version{Major: 0, Minor: 17, Patch: 3}
	tests := []struct {
		other Version
		want  bool
	}{
		{Version{0, 17, 3}, true},
		{Version{0, 17, 4}, false},
		{Version{0, 16, 9}, true},
		{Version{0, 18, 0}, false},
		{Version{1, 0, 0}, false},
		{Version{0, 9, 0}, true},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, v.AtLeast(tt.other), tt.other.String())
	}
	assert.True(t, Version{Major: 1}.AtLeast(Version{Minor: 20}))
}
