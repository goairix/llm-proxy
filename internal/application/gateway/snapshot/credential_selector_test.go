package snapshot

import (
	"sync"
	"testing"

	"github.com/google/uuid"

	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
)

func TestCredentialSelectorSupportsConcurrentRoundRobin(t *testing.T) {
	source := validOpenAISourceConfig(t)
	providerID := source.Providers[0].ID
	projectID := source.Projects[0].ID
	first, _ := newProjectCredential(providerID, projectID, 50)
	second, _ := newProjectCredential(providerID, projectID, 51)
	source.Credentials = append(source.Credentials, *first, *second)
	compiled, err := NewCompiler().Compile(source, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	selector := NewCredentialSelector()
	session := Session{snapshot: compiled}
	access := AccessContext{OrganizationID: source.Organizations[0].ID, ProjectID: projectID}

	const calls = 100
	counts := make(map[string]int)
	var countsMu sync.Mutex
	var wait sync.WaitGroup
	for range calls {
		wait.Add(1)
		go func() {
			defer wait.Done()
			selected, err := selector.Select(session, access, providerID)
			if err != nil {
				t.Errorf("Select() error=%v", err)
				return
			}
			countsMu.Lock()
			counts[selected.CredentialID.String()]++
			countsMu.Unlock()
		}()
	}
	wait.Wait()
	if counts[first.ID.String()] != calls/2 || counts[second.ID.String()] != calls/2 {
		t.Fatalf("counts=%v", counts)
	}
}

func newProjectCredential(providerID, projectID uuid.UUID, ciphertext byte) (*catalogmodel.ProviderCredential, error) {
	return catalogmodel.NewProviderCredential(
		providerID,
		catalogmodel.Scope{Kind: catalogmodel.ScopeProject, ProjectID: projectID},
		sealedWithCiphertext(ciphertext),
	)
}
