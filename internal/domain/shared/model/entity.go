package model

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Entity contains identity and lifecycle timestamps shared by domain entities.
type Entity struct {
	ID        uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewEntity creates an entity with a UUIDv7 identity and UTC timestamps.
func NewEntity() (Entity, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Entity{}, fmt.Errorf("generate uuidv7: %w", err)
	}
	now := time.Now().UTC()
	return Entity{ID: id, CreatedAt: now, UpdatedAt: now}, nil
}
