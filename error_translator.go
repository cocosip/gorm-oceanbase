package oceanbase

import (
	"errors"

	"github.com/cocosip/go-oceanbase/dberrors"
	"gorm.io/gorm"
)

// MySQL protocol error codes reported by OceanBase's MySQL compatibility mode.
const (
	mysqlErrDuplicatedKey      = 1062
	mysqlErrForeignKeyViolated = 1451
	mysqlErrChildRecordFound   = 1452
)

// Translate implements gorm.ErrorTranslator. It only maps server error codes
// the driver preserved in *dberrors.Error; anything else passes through
// untouched so callers see the original cause.
func (d *Dialector) Translate(err error) error {
	if err == nil {
		return nil
	}
	var serverErr *dberrors.Error
	if !errors.As(err, &serverErr) {
		return err
	}
	switch serverErr.Code {
	case mysqlErrDuplicatedKey:
		return gorm.ErrDuplicatedKey
	case mysqlErrForeignKeyViolated, mysqlErrChildRecordFound:
		return gorm.ErrForeignKeyViolated
	default:
		return err
	}
}

var _ gorm.ErrorTranslator = (*Dialector)(nil)
