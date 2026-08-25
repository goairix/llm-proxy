package entity

import (
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

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
