package model

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
)

func TestNewOrganizationAndProjectUseUUIDv7(t *testing.T) {
	organization, err := NewOrganization("Acme")
	if err != nil {
		t.Fatal(err)
	}
	project, err := NewProject(organization.ID, "Production")
	if err != nil {
		t.Fatal(err)
	}

	if organization.ID.Version() != 7 || project.ID.Version() != 7 {
		t.Fatalf("versions = %d, %d; want 7, 7", organization.ID.Version(), project.ID.Version())
	}
	if organization.Status != sharedmodel.StatusActive || project.Status != sharedmodel.StatusActive {
		t.Fatalf("statuses = %q, %q; want active", organization.Status, project.Status)
	}
	if organization.CreatedAt.IsZero() || organization.UpdatedAt.IsZero() {
		t.Fatal("organization timestamps must be initialized")
	}
}

func TestTenancyConstructorsRejectInvalidInput(t *testing.T) {
	projectID := uuid.Must(uuid.NewV7())
	tests := []struct {
		name string
		new  func() error
	}{
		{name: "blank organization name", new: func() error { _, err := NewOrganization("  "); return err }},
		{name: "nil organization id", new: func() error { _, err := NewProject(uuid.Nil, "Production"); return err }},
		{name: "blank project name", new: func() error { _, err := NewProject(projectID, " "); return err }},
		{name: "zero virtual key hash", new: func() error {
			_, err := NewVirtualKey(projectID, "ci", [32]byte{}, "llmp_v1_abc", "1234", nil)
			return err
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.new(); !errors.Is(err, sharederrors.ErrInvalid) {
				t.Fatalf("error = %v; want ErrInvalid", err)
			}
		})
	}
}

func TestVirtualKeyActiveAt(t *testing.T) {
	now := time.Now().UTC()
	projectID := uuid.Must(uuid.NewV7())
	hash := [32]byte{1}

	active, err := NewVirtualKey(projectID, "ci", hash, "llmp_v1_abc", "1234", nil)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID.Version() != 7 || !active.ActiveAt(now) {
		t.Fatalf("new virtual key = %+v; want active UUIDv7 key", active)
	}

	expires := now.Add(-time.Minute)
	expired, err := NewVirtualKey(projectID, "expired", hash, "llmp_v1_def", "5678", &expires)
	if err != nil {
		t.Fatal(err)
	}
	if expired.ActiveAt(now) {
		t.Fatal("expired key is active")
	}

	active.Status = sharedmodel.StatusDisabled
	if active.ActiveAt(now) {
		t.Fatal("disabled key is active")
	}
}
