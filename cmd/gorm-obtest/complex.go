package main

import (
	"strings"

	obtypes "github.com/cocosip/go-oceanbase/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *scenario) createType(definition string) (string, error) {
	name := strings.ToUpper(s.name())
	if err := s.db.Exec("CREATE TYPE " + s.quote(name) + " " + definition).Error; err != nil {
		return "", err
	}
	s.objects = append(s.objects, name)
	s.cleanups = append(s.cleanups, cleanupAction{name: name, run: func(db *gorm.DB) error { return db.Exec("DROP TYPE ?", clause.Column{Name: name}).Error }})
	return name, nil
}

func runObject(s *scenario) error {
	name, err := s.createType("AS OBJECT(N NUMBER(38,9), V VARCHAR2(64))")
	if err != nil {
		return err
	}
	value := obtypes.Object{Type: name, Fields: []obtypes.Attribute{{Name: "N", Value: obtypes.Number(decimalSample)}, {Name: "V", Value: "object-value"}}}
	f := scalar("oracle_object", modeOracle, "type:"+name, value)
	f.compare = func(_, got any) bool {
		object, ok := got.(obtypes.Object)
		if !ok || object.Type != name || len(object.Fields) != 2 || object.Fields[0].Name != "N" || object.Fields[1].Name != "V" {
			return false
		}
		number, ok := object.Fields[0].Value.(obtypes.Number)
		return ok && equalDecimal(obtypes.Number(decimalSample), number) && object.Fields[1].Value == "object-value"
	}
	return scalarCase(f, false).run(s)
}
