package oceanbase

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	obMarker      = regexp.MustCompile(`(?i)oceanbase`)
	obVersionExpr = regexp.MustCompile(`(?i)oceanbase[^0-9]*v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:\.[0-9]+)*(?:[^0-9.]|$)`)
	plainVersion  = regexp.MustCompile(`(?i)(?:^|[^0-9a-z])v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:\.[0-9]+)*(?:[^0-9.]|$)`)
)

// serverVersion is the OceanBase product version extracted from a server
// version string, never the MySQL compatibility prefix.
type serverVersion struct {
	major, minor, patch int
	valid               bool
}

func (v serverVersion) atLeast(major, minor, patch int) bool {
	if !v.valid {
		return false
	}
	if v.major != major {
		return v.major > major
	}
	if v.minor != minor {
		return v.minor > minor
	}
	return v.patch >= patch
}

// parseOBVersion parses the authoritative ob_version() result. Some builds
// return a bare version, so the OceanBase marker is optional.
func parseOBVersion(raw string) serverVersion {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return serverVersion{}
	}
	if obMarker.MatchString(raw) {
		return matchVersion(obVersionExpr.FindStringSubmatch(raw))
	}
	return matchVersion(plainVersion.FindStringSubmatch(raw))
}

// parseCompatibleVersion parses a MySQL VERSION() fallback. Without an
// OceanBase marker the value may describe a MySQL-compatible prefix, so it is
// treated as unknown instead of assuming an OceanBase release.
func parseCompatibleVersion(raw string) serverVersion {
	raw = strings.TrimSpace(raw)
	if !obMarker.MatchString(raw) {
		return serverVersion{}
	}
	return matchVersion(obVersionExpr.FindStringSubmatch(raw))
}

func matchVersion(parts []string) serverVersion {
	if len(parts) != 4 {
		return serverVersion{}
	}
	numbers := make([]int, 3)
	for i := range numbers {
		n, err := strconv.Atoi(parts[i+1])
		if err != nil {
			return serverVersion{}
		}
		numbers[i] = n
	}
	return serverVersion{major: numbers[0], minor: numbers[1], patch: numbers[2], valid: true}
}

// capabilities are the client-syntax gates of one pinned mode.
type capabilities struct {
	dontSupportRenameColumn   bool
	dontSupportForShareClause bool
	dontSupportDropConstraint bool
}

// mysqlCapabilities maps OceanBase's MySQL mode onto client syntax gates.
// OceanBase's MySQL mode reports the MySQL 5.7 compatibility profile, so
// syntax introduced only in MySQL 8 stays disabled. Unknown versions keep the
// same conservative gates; a verified release may relax a gate later.
//
// ponytail: MySQL 8-only syntax is always rewritten; flip a gate per verified
// OceanBase release once the integration matrix has evidence.
func mysqlCapabilities() capabilities {
	return capabilities{
		dontSupportRenameColumn:   true,
		dontSupportForShareClause: true,
		dontSupportDropConstraint: true,
	}
}
