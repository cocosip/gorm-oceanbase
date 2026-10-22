package oceanbase

import "gorm.io/gorm/clause"

// writeString writes to a clause.Writer. SQL builders cannot recover from a
// write error through this interface; GORM's own writers never fail, so the
// error is intentionally discarded here in one place instead of at each call.
func writeString(writer clause.Writer, value string) {
	_, _ = writer.WriteString(value)
}

// writeByte writes one byte to a clause.Writer.
func writeByte(writer clause.Writer, value byte) {
	_ = writer.WriteByte(value)
}
