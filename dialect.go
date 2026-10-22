package oceanbase

import (
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// QuoteTo dispatches identifier quoting to the pinned mode.
func (d *Dialector) QuoteTo(writer clause.Writer, str string) {
	mysqlQuoteTo(writer, str)
}

// DataTypeOf dispatches column type mapping to the pinned mode.
func (d *Dialector) DataTypeOf(field *schema.Field) string {
	return mysqlDataTypeOf(d, field)
}
