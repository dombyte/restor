package restic

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// The restic releases restor supports. The integration tests (internal/app, build tag
// integration) run against both in CI; Renovate raises newestTested with the newest
// release once they pass. minSupported is the oldest release that passes them: 0.17.1
// brought the exit code for a wrong password, 0.17.0 --skip-if-unchanged, and older
// releases lack backup --group-by.
const (
	minSupported = "0.17.1"
	newestTested = "0.19.1" // renovate: datasource=github-releases depName=restic/restic
)

// ErrVersion is returned by ParseVersion for output without a restic version.
var ErrVersion = errors.New("restic: cannot read version")

// Supported returns the oldest restic release restor works with and the newest one it
// was tested with.
func Supported() (oldest, newest Version) {
	// Both are constants that TestSupported parses.
	oldest, _ = ParseVersion("restic " + minSupported) //nolint:errcheck // see above
	newest, _ = ParseVersion("restic " + newestTested) //nolint:errcheck // see above
	return oldest, newest
}

// Version is a restic release, e.g. 0.17.3.
type Version struct {
	Major, Minor, Patch int
}

// ParseVersion reads the version from the output of `restic version`, e.g. "restic 0.17.3
// compiled with go1.23.1 on linux/amd64".
func ParseVersion(out string) (Version, error) {
	m := regexp.MustCompile(`restic (\d+)\.(\d+)\.(\d+)`).FindStringSubmatch(out)
	if m == nil {
		return Version{}, fmt.Errorf("%w: %q", ErrVersion, out)
	}
	var parts [3]int
	for i := range parts {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return Version{}, fmt.Errorf("%w: %w", ErrVersion, err)
		}
		parts[i] = n
	}
	return Version{Major: parts[0], Minor: parts[1], Patch: parts[2]}, nil
}

// AtLeast reports whether v is o or newer.
func (v Version) AtLeast(o Version) bool {
	if v.Major != o.Major {
		return v.Major > o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor > o.Minor
	}
	return v.Patch >= o.Patch
}

func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}
