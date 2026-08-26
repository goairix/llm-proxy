package entity

import (
	"reflect"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

func TestBaseEntityStorageContract(t *testing.T) {
	typeOfBase := reflect.TypeOf(BaseEntity{})
	id, _ := typeOfBase.FieldByName("ID")
	if tag := id.Tag.Get("gorm"); !strings.Contains(tag, "default:uuid_generate_v7()") {
		t.Fatalf("ID gorm tag = %q; want uuid_generate_v7 default", tag)
	}
	for _, fieldName := range []string{"CreatedAt", "UpdatedAt"} {
		field, _ := typeOfBase.FieldByName(fieldName)
		if tag := field.Tag.Get("gorm"); !strings.Contains(tag, "type:timestamp(0) without time zone") {
			t.Fatalf("%s gorm tag = %q; want timestamp(0) without time zone", fieldName, tag)
		}
	}
}

func TestPersistenceEntitiesDeclareNoGORMRelationships(t *testing.T) {
	models := []any{
		&Organization{},
		&Project{},
		&VirtualKey{},
		&Provider{},
		&ProviderCredential{},
		&Deployment{},
		&ModelAlias{},
		&RouteTarget{},
		&ConfigRevision{},
	}
	for _, model := range models {
		parsed, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
		if err != nil {
			t.Fatalf("parse %T: %v", model, err)
		}
		if len(parsed.Relationships.Relations) != 0 {
			t.Errorf("%T declares GORM relationships: %v", model, parsed.Relationships.Relations)
		}
	}
}
