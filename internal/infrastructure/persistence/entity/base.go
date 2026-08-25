// Package entity defines database records separately from domain models.
package entity

import (
	"time"

	"github.com/google/uuid"
)

// BaseEntity is the UUIDv7 and audit-time shape shared by persisted resources.
// UUIDs are generated in Go before persistence; the database has no UUID default.
type BaseEntity struct {
	ID        uuid.UUID `gorm:"type:uuid;not null;default:uuid_generate_v7();primary_key"`
	CreatedAt time.Time `gorm:"type:timestamp(0) without time zone;index;not null"`
	UpdatedAt time.Time `gorm:"type:timestamp(0) without time zone;not null"`
}
