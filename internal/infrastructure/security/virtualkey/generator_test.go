package virtualkey

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
)

func TestGeneratorCreatesHashedVersionedKey(t *testing.T) {
	generator := NewGenerator()
	first, err := generator.Generate()
	if err != nil {
		t.Fatal(err)
	}
	second, err := generator.Generate()
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(first.Plaintext, "llmp_v1_") {
		t.Fatalf("plaintext = %q", first.Plaintext)
	}
	body := strings.TrimPrefix(first.Plaintext, "llmp_v1_")
	if len(body) < 43 {
		t.Fatalf("encoded random body length = %d; want at least 43", len(body))
	}
	wantHash := sha256.Sum256([]byte(first.Plaintext))
	if first.Hash != wantHash {
		t.Fatal("hash does not match complete plaintext key")
	}
	if !strings.HasPrefix(first.Plaintext, first.Prefix) || first.LastFour != first.Plaintext[len(first.Plaintext)-4:] {
		t.Fatalf("display metadata = %q, %q", first.Prefix, first.LastFour)
	}
	if first.Plaintext == second.Plaintext {
		t.Fatal("two generated keys are identical")
	}
}

func TestGeneratorPropagatesRandomSourceFailure(t *testing.T) {
	generator := NewGeneratorWithReader(failingReader{})
	if _, err := generator.Generate(); err == nil {
		t.Fatal("random source failure was ignored")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("random offline") }
