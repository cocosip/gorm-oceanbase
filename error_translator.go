package oceanbase

import (
	"errors"

	ob "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/dberrors"
	"gorm.io/gorm"
)

// MySQL protocol error codes reported by OceanBase's MySQL compatibility mode.
const (
	mysqlErrDuplicatedKey      = 1062
	mysqlErrForeignKeyViolated = 1451
	mysqlErrChildRecordFound   = 1452
)

// ORA error codes reported by OceanBase's Oracle compatibility mode; the
// driver preserves the ORA-* number in dberrors.Error.Code.
const (
	oracleErrDuplicatedKey      = 1    // ORA-00001 unique constraint violated
	oracleErrForeignKeyViolated = 2291 // ORA-02291 parent key not found
	oracleErrChildRecordFound   = 2292 // ORA-02292 child record found
)

// Translate implements gorm.ErrorTranslator. It consults the code table of the
// pinned mode only, so the two modes' codes stay isolated, and passes every
// unmatched error through untouched so callers see the original cause.
func (d *Dialector) Translate(err error) error {
	if err == nil {
		return nil
	}
	var serverErr *dberrors.Error
	if !errors.As(err, &serverErr) {
		return err
	}
	switch d.mode() {
	case ob.ModeOracle:
		switch serverErr.Code {
		case oracleErrDuplicatedKey:
			return gorm.ErrDuplicatedKey
		case oracleErrForeignKeyViolated, oracleErrChildRecordFound:
			return gorm.ErrForeignKeyViolated
		}
	default:
		switch serverErr.Code {
		case mysqlErrDuplicatedKey:
			return gorm.ErrDuplicatedKey
		case mysqlErrForeignKeyViolated, mysqlErrChildRecordFound:
			return gorm.ErrForeignKeyViolated
		}
	}
	return err
}

var _ gorm.ErrorTranslator = (*Dialector)(nil)
