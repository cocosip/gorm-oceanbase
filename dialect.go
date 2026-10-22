package oceanbase

import (
	ob "github.com/cocosip/go-oceanbase"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// DefaultValueOf dispatches the database-default marker to the pinned mode.
// MySQL renders the DEFAULT keyword; Oracle returns a marker the Oracle create
// callback consumes by omitting the column (design.md §5.3).
func (d *Dialector) DefaultValueOf(*schema.Field) clause.Expression {
	if d.mode() == ob.ModeOracle {
		return oracleDefaultMarker{}
	}
	return clause.Expr{SQL: "DEFAULT"}
}

// QuoteTo dispatches identifier quoting to the pinned mode.
func (d *Dialector) QuoteTo(writer clause.Writer, str string) {
	if d.mode() == ob.ModeOracle {
		oracleQuoteTo(writer, str)
		return
	}
	mysqlQuoteTo(writer, str)
}

// DataTypeOf dispatches column type mapping to the pinned mode.
func (d *Dialector) DataTypeOf(field *schema.Field) string {
	if d.mode() == ob.ModeOracle {
		return oracleDataTypeOf(d, field)
	}
	return mysqlDataTypeOf(d, field)
}
