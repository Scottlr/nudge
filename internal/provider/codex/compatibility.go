package codex

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	ErrUnsupportedVersion = errors.New("codex app-server version is unsupported")
	ErrMissingVersion     = errors.New("codex app-server version is missing")
)

// Version is the bounded semantic version reported by a Codex app-server.
type Version struct {
	Major      int
	Minor      int
	Patch      int
	Prerelease string
}

func (v Version) String() string {
	value := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Prerelease != "" {
		value += "-" + v.Prerelease
	}
	return value
}

func (v Version) compare(other Version) int {
	if v.Major != other.Major {
		return compareInt(v.Major, other.Major)
	}
	if v.Minor != other.Minor {
		return compareInt(v.Minor, other.Minor)
	}
	if v.Patch != other.Patch {
		return compareInt(v.Patch, other.Patch)
	}
	if v.Prerelease == other.Prerelease {
		return 0
	}
	if v.Prerelease == "" {
		return 1
	}
	if other.Prerelease == "" {
		return -1
	}
	return strings.Compare(v.Prerelease, other.Prerelease)
}

func compareInt(left, right int) int {
	if left < right {
		return -1
	}
	return 1
}

// CompatibilityPolicy bounds the stable, non-experimental schema range.
type CompatibilityPolicy struct {
	Minimum Version
	Maximum Version
}

// DefaultCompatibilityPolicy accepts the pinned 0.144 stable API series.
func DefaultCompatibilityPolicy() CompatibilityPolicy {
	return CompatibilityPolicy{
		Minimum: Version{Major: 0, Minor: 144, Patch: 0, Prerelease: "alpha.4"},
		Maximum: Version{Major: 0, Minor: 145, Patch: 0, Prerelease: ""},
	}
}

func (p CompatibilityPolicy) validate() error {
	if p.Minimum.compare(p.Maximum) >= 0 {
		return errors.New("invalid codex compatibility range")
	}
	return nil
}

// ParseVersion extracts a semantic version from a Codex user-agent value.
// It accepts the current user-agent forms while ignoring bounded build text.
func ParseVersion(userAgent string) (Version, error) {
	match := versionPattern.FindStringSubmatch(userAgent)
	if len(match) != 5 {
		return Version{}, ErrMissingVersion
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	patch, _ := strconv.Atoi(match[3])
	return Version{Major: major, Minor: minor, Patch: patch, Prerelease: match[4]}, nil
}

var versionPattern = regexp.MustCompile(`(?:^|[^0-9])([0-9]+)\.([0-9]+)\.([0-9]+)(?:-([0-9A-Za-z.-]+))?(?:[^0-9A-Za-z.-]|$)`)

// Check accepts a user-agent only when the reported version is within the
// configured stable range. The error contains no provider response body.
func (p CompatibilityPolicy) Check(userAgent string) (Version, error) {
	if err := p.validate(); err != nil {
		return Version{}, err
	}
	version, err := ParseVersion(userAgent)
	if err != nil {
		return Version{}, err
	}
	if version.compare(p.Minimum) < 0 || version.compare(p.Maximum) >= 0 {
		return Version{}, fmt.Errorf("%w: %s outside %s..%s", ErrUnsupportedVersion, version, p.Minimum, p.Maximum)
	}
	return version, nil
}
