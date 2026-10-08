package util

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errTest = errors.New("test: missing dependency")

func TestRequireAll(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		reqs []Requirement
		want string
	}{
		{name: "all present", reqs: []Requirement{{"A", true}, {"B", true}}},
		{
			name: "first missing wins", reqs: []Requirement{{"A", true}, {"B", false}, {"C", false}},
			want: "B",
		},
		{name: "none", reqs: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := RequireAll(errTest, tt.reqs...)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, errTest)
			var depErr *DependencyError
			require.ErrorAs(t, err, &depErr)
			assert.Equal(t, tt.want, depErr.Dependency)
			assert.Equal(t, "test: missing dependency: "+tt.want, err.Error())
		})
	}
}
