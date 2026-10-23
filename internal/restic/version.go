package restic

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// ErrVersion is returned by ParseVersion for output without a restic version.
var ErrVersion = errors.New("restic: cannot read version")

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
