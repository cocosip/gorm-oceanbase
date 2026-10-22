package oceanbase

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
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

// Oracle column type limits. Extended (32k) VARCHAR2 and RAW sizes depend on
// server settings, so the standard limits decide the LOB switch.
const (
	// oracleVarchar2Max is the standard VARCHAR2 byte limit.
	oracleVarchar2Max = 4000
	// oracleRawMax is the standard RAW byte limit.
	oracleRawMax = 2000
	// oracleDefaultStringSize is the fallback length for an unsized string.
	oracleDefaultStringSize = 512
)

// oracleDataTypeOf maps a schema field to its Oracle-compatible column type.
func oracleDataTypeOf(d *Dialector, field *schema.Field) string {
	switch field.DataType {
	case schema.Bool:
		return "NUMBER(1)"
	case schema.Int, schema.Uint:
		return oracleNumberType(field)
	case schema.Float:
		return d.oracleFloatType(field)
	case schema.String:
		return d.oracleStringType(field)
	case schema.Time:
		return d.oracleTimeType(field)
	case schema.Bytes:
		return d.oracleBytesType(field)
	default:
		// Complex types (types.Object/Array) and named server types cannot be
		// inferred from the Go type; they need an explicit gorm:"type:..." tag
		// and are passed through verbatim (design.md §5.2).
		return string(field.DataType)
	}
}

// oracleNumberType maps the field bit size to a NUMBER precision. int64 needs
// 19 digits, uint64 needs 20 (max 18446744073709551615).
func oracleNumberType(field *schema.Field) string {
	var precision int
	switch {
	case field.Size <= 8:
		precision = 3
	case field.Size <= 16:
		precision = 5
	case field.Size <= 32:
		precision = 10
	case field.DataType == schema.Uint:
		precision = 20
	default:
		precision = 19
	}
	return fmt.Sprintf("NUMBER(%d,0)", precision)
}

func (d *Dialector) oracleFloatType(field *schema.Field) string {
	if field.Precision > 0 {
		return fmt.Sprintf("NUMBER(%d,%d)", field.Precision, field.Scale)
	}
	if field.Size <= 32 {
		return "BINARY_FLOAT"
	}
	return "BINARY_DOUBLE"
}

func (d *Dialector) oracleStringType(field *schema.Field) string {
	size := field.Size
	if size == 0 {
		if d.DefaultStringSize > 0 {
			size = int(d.DefaultStringSize)
		} else {
			size = oracleDefaultStringSize
		}
	}
	if size > oracleVarchar2Max {
		return "CLOB"
	}
	return fmt.Sprintf("VARCHAR2(%d)", size)
}

// oracleTimeType renders TIMESTAMP with the field precision; the default
// matches the Oracle TIMESTAMP default of microseconds. TIME ZONE variants are
// chosen with an explicit type tag and need the driver's Scanner types when
// read back (design.md §5.2).
func (d *Dialector) oracleTimeType(field *schema.Field) string {
	if field.Precision == 0 && field.TagSettings["PRECISION"] == "" {
		field.Precision = oracleDatetimePrecision
	}
	if field.Precision > 0 {
		return fmt.Sprintf("TIMESTAMP(%d)", field.Precision)
	}
	return "TIMESTAMP"
}

func (d *Dialector) oracleBytesType(field *schema.Field) string {
	if field.Size > 0 && field.Size <= oracleRawMax {
		return fmt.Sprintf("RAW(%d)", field.Size)
	}
	return "BLOB"
}
