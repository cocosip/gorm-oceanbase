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
// syntax introduced only in MySQL 8 stays rewritten; the gates are
// version-independent today. Flip a gate per verified OceanBase release once
// the integration matrix has evidence.
func mysqlCapabilities() capabilities {
	return capabilities{
		dontSupportRenameColumn:   true,
		dontSupportForShareClause: true,
		dontSupportDropConstraint: true,
	}
}

// oracleIdentityMinVersion is the candidate minimum OceanBase version for
// GENERATED ... AS IDENTITY support. The threshold still awaits real-tenant
// verification (design.md §6.1); unknown versions keep IDENTITY disabled, and
// the Oracle migrator refuses the DDL before executing it.
var oracleIdentityMinVersion = serverVersion{major: 4, minor: 2, patch: 1, valid: true}

// oracleIdentitySupported reports whether IDENTITY DDL may run for the
// dialector's server version. Unknown or lower versions return false so the
// caller can reject before executing any DDL.
func (d *Dialector) oracleIdentitySupported() bool {
	return parseOBVersion(d.ServerVersion).atLeast(
		oracleIdentityMinVersion.major,
		oracleIdentityMinVersion.minor,
		oracleIdentityMinVersion.patch,
	)
}
