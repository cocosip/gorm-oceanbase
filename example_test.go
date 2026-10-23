package oceanbase_test

import (
	ob "github.com/cocosip/go-oceanbase"
	oceanbase "github.com/cocosip/gorm-oceanbase"
	"gorm.io/gorm"
)

func ExampleNew() {
	cfg, err := ob.ParseDSN("oceanbase://user:pass@127.0.0.1:2881/db")
	if err != nil {
		panic(err)
	}
	_, err = gorm.Open(oceanbase.New(oceanbase.Config{
		DSNConfig: &cfg,
		Mode:      ob.ModeOracle,
	}), &gorm.Config{})
	if err != nil {
		panic(err)
	}
}
