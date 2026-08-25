package virtualkey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"

	controlport "github.com/goairix/llm-proxy/internal/application/controlplane/port"
)

const (
	keyPrefix           = "llmp_v1_"
	randomBytes         = 32
	displayBodyLength   = 8
	displaySuffixLength = 4
)

type Generator struct{ random io.Reader }

func NewGenerator() *Generator {
	return &Generator{random: rand.Reader}
}

func NewGeneratorWithReader(random io.Reader) *Generator {
	return &Generator{random: random}
}

func (g *Generator) Generate() (controlport.GeneratedVirtualKey, error) {
	if g == nil || g.random == nil {
		return controlport.GeneratedVirtualKey{}, fmt.Errorf("virtual key random source is unavailable")
	}
	random := make([]byte, randomBytes)
	if _, err := io.ReadFull(g.random, random); err != nil {
		return controlport.GeneratedVirtualKey{}, fmt.Errorf("generate virtual key entropy: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(random)
	plaintext := keyPrefix + body
	hash := sha256.Sum256([]byte(plaintext))
	return controlport.GeneratedVirtualKey{
		Plaintext: plaintext,
		Hash:      hash,
		Prefix:    keyPrefix + body[:displayBodyLength],
		LastFour:  plaintext[len(plaintext)-displaySuffixLength:],
	}, nil
}

var _ controlport.VirtualKeyGenerator = (*Generator)(nil)
