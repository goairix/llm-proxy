package snapshot

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
)

func TestAuthenticatorAcceptsValidVirtualKey(t *testing.T) {
	token := "llmp_v1_valid-secret"
	projectID := uuid.Must(uuid.NewV7())
	organizationID := uuid.Must(uuid.NewV7())
	virtualKeyID := uuid.Must(uuid.NewV7())
	snapshot := authenticatedSnapshot(token, projectID, organizationID, virtualKeyID, fixedNow.Add(time.Hour))
	store := NewStore()
	store.Publish(snapshot)

	session, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	access, err := session.Authenticate(token, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if access.ProjectID != projectID || access.OrganizationID != organizationID || access.VirtualKeyID != virtualKeyID {
		t.Fatalf("access = %+v", access)
	}
}

func TestAuthenticatorRejectsInvalidVirtualKeysWithoutLeakingToken(t *testing.T) {
	validToken := "llmp_v1_valid-secret"
	projectID := uuid.Must(uuid.NewV7())
	snapshot := authenticatedSnapshot(validToken, projectID, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), fixedNow.Add(time.Hour))
	store := NewStore()
	store.Publish(snapshot)
	session, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		token string
		now   time.Time
	}{
		{name: "empty", token: "", now: fixedNow},
		{name: "unknown", token: "llmp_v1_wrong-secret", now: fixedNow},
		{name: "expired", token: validToken, now: fixedNow.Add(2 * time.Hour)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := session.Authenticate(test.token, test.now)
			if !errors.Is(err, ErrInvalidVirtualKey) {
				t.Fatalf("error = %v, want ErrInvalidVirtualKey", err)
			}
			if test.token != "" && strings.Contains(err.Error(), test.token) {
				t.Fatalf("error leaked virtual key: %v", err)
			}
		})
	}
}

func TestSessionResolveReturnsDeepCopy(t *testing.T) {
	projectID := uuid.Must(uuid.NewV7())
	key := RouteKey{ProjectID: projectID, Model: "assistant"}
	wantCiphertext := byte(4)
	snapshot := newSnapshotForTest(9)
	snapshot.routes[key] = RoutePlan{
		AliasID: uuid.Must(uuid.NewV7()),
		Alias:   "assistant",
		Deployment: Deployment{
			ID:            uuid.Must(uuid.NewV7()),
			ConnectorType: "openai",
			Credential: &CredentialEnvelope{
				CredentialID: uuid.Must(uuid.NewV7()),
				Sealed: catalogmodel.SealedCredential{
					KeyVersion: "v1", WrappedKeyNonce: []byte{1}, WrappedDataKey: []byte{2},
					PayloadNonce: []byte{3}, Ciphertext: []byte{wantCiphertext},
				},
			},
		},
	}
	store := NewStore()
	store.Publish(snapshot)
	session, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}

	first, err := session.Resolve(projectID, "assistant")
	if err != nil {
		t.Fatal(err)
	}
	first.Deployment.Credential.Sealed.Ciphertext[0] = 99
	second, err := session.Resolve(projectID, "assistant")
	if err != nil {
		t.Fatal(err)
	}
	if second.Deployment.Credential.Sealed.Ciphertext[0] != wantCiphertext {
		t.Fatal("resolved route plan modified the published snapshot")
	}
}

func TestSessionResolveUsesExactModelAndProject(t *testing.T) {
	projectID := uuid.Must(uuid.NewV7())
	snapshot := newSnapshotForTest(1)
	snapshot.routes[RouteKey{ProjectID: projectID, Model: "Assistant"}] = RoutePlan{Alias: "Assistant"}
	store := NewStore()
	store.Publish(snapshot)
	session, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}

	for _, key := range []RouteKey{
		{ProjectID: projectID, Model: "assistant"},
		{ProjectID: uuid.Must(uuid.NewV7()), Model: "Assistant"},
	} {
		if _, err := session.Resolve(key.ProjectID, key.Model); !errors.Is(err, ErrRouteNotFound) {
			t.Fatalf("Resolve(%s, %q) error = %v", key.ProjectID, key.Model, err)
		}
	}
}

func TestStoreBeginRequiresPublishedSnapshot(t *testing.T) {
	_, err := NewStore().Begin()
	if !errors.Is(err, ErrSnapshotUnavailable) {
		t.Fatalf("error = %v, want ErrSnapshotUnavailable", err)
	}
}

func authenticatedSnapshot(token string, projectID, organizationID, virtualKeyID uuid.UUID, expiresAt time.Time) *RuntimeSnapshot {
	result := newSnapshotForTest(1)
	result.virtualKeys[sha256.Sum256([]byte(token))] = AccessContext{
		ProjectID: projectID, OrganizationID: organizationID, VirtualKeyID: virtualKeyID,
		ExpiresAt: expiresAt, HasExpiry: true,
	}
	return result
}
