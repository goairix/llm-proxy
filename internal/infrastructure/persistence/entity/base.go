// Package entity defines database records separately from domain models.
package entity

import (
	"time"

	"github.com/google/uuid"
)

// BaseEntity is the UUIDv7 and audit-time shape shared by persisted resources.
// Application writes generate UUIDv7 in Go; the database default supports manual inserts.
type BaseEntity struct {
	ID        uuid.UUID `gorm:"type:uuid;not null;default:uuid_generate_v7();primary_key"`
	CreatedAt time.Time `gorm:"type:timestamp(0) without time zone;index;not null"`
	UpdatedAt time.Time `gorm:"type:timestamp(0) without time zone;not null"`
}
