package oceanbase

import (
	ob "github.com/cocosip/go-oceanbase"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

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
	return mysqlDataTypeOf(d, field)
}
