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
