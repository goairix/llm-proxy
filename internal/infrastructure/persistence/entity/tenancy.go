package entity

import (
	"time"

	"github.com/google/uuid"
)

type Organization struct {
	BaseEntity
	Name   string `gorm:"type:varchar(255);not null"`
	Status string `gorm:"type:varchar(32);not null;index:idx_organizations_status"`
}

func (Organization) TableName() string { return "organizations" }

type Project struct {
	BaseEntity
	OrganizationID uuid.UUID    `gorm:"type:uuid;not null;index:idx_projects_organization_id"`
	Organization   Organization `gorm:"foreignKey:OrganizationID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
	Name           string       `gorm:"type:varchar(255);not null"`
	Status         string       `gorm:"type:varchar(32);not null;index:idx_projects_status"`
}

func (Project) TableName() string { return "projects" }

type VirtualKey struct {
	BaseEntity
	ProjectID uuid.UUID  `gorm:"type:uuid;not null;index:idx_virtual_keys_project_id"`
	Project   Project    `gorm:"foreignKey:ProjectID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
	Name      string     `gorm:"type:varchar(255);not null"`
	Hash      []byte     `gorm:"type:bytea;not null;uniqueIndex:ux_virtual_keys_hash"`
	Prefix    string     `gorm:"type:varchar(64);not null"`
	LastFour  string     `gorm:"type:char(4);not null"`
	Status    string     `gorm:"type:varchar(32);not null;index:idx_virtual_keys_status"`
	ExpiresAt *time.Time `gorm:"type:timestamptz;index:idx_virtual_keys_expires_at"`
}

func (VirtualKey) TableName() string { return "virtual_keys" }
