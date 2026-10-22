package oceanbase

import (
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
)

// registerOracleCallbacks installs the Oracle compatibility mode callback set.
// It keeps the default transaction, model hook and association callbacks. The
// gorm:create replacement that builds and executes whole INSERT/INSERT ALL/
// MERGE statements lands with the Oracle Create callback (design.md §5.3).
func registerOracleCallbacks(db *gorm.DB) {
	callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{})
}

// oracleClauseBuilders returns the Oracle clause rewrites. The LIMIT rewrite
// to OFFSET...FETCH FIRST arrives with the Oracle LIMIT support task.
func oracleClauseBuilders(_ *Dialector) map[string]clause.ClauseBuilder {
	return map[string]clause.ClauseBuilder{}
}

// oracleQuoteTo quotes identifiers with double quotes. OceanBase's Oracle mode
// folds unquoted identifiers to upper case, so every identifier part that does
// not carry its own quotes is written upper case; a part that arrives
// self-quoted (an explicit-case name) is preserved verbatim, and embedded
// quotes are escaped by doubling. The state machine mirrors the MySQL one.
func oracleQuoteTo(writer clause.Writer, str string) {
	var (
		underQuoted, selfQuoted bool
		continuousQuote         int8
		shiftDelimiter          int8
	)

	for _, v := range []byte(str) {
		switch v {
		case '"':
			continuousQuote++
			if continuousQuote == 2 {
				writeString(writer, `""`)
				continuousQuote = 0
			}
		case '.':
			if continuousQuote > 0 || !selfQuoted {
				shiftDelimiter = 0
				underQuoted = false
				continuousQuote = 0
				writeByte(writer, '"')
			}
			writeByte(writer, v)
			continue
		default:
			if shiftDelimiter-continuousQuote <= 0 && !underQuoted {
				writeByte(writer, '"')
				underQuoted = true
				if selfQuoted = continuousQuote > 0; selfQuoted {
					continuousQuote--
				}
			}

			for ; continuousQuote > 0; continuousQuote-- {
				writeString(writer, `""`)
			}

			if selfQuoted {
				writeByte(writer, v)
			} else {
				writeByte(writer, asciiUpper(v))
			}
		}
		shiftDelimiter++
	}

	if continuousQuote > 0 && !selfQuoted {
		writeString(writer, `""`)
	}
	writeByte(writer, '"')
}

// asciiUpper uppercases one ASCII letter byte. UTF-8 continuation bytes stay
// untouched so multi-byte identifiers are not corrupted.
func asciiUpper(v byte) byte {
	if v >= 'a' && v <= 'z' {
		return v - ('a' - 'A')
	}
	return v
}
