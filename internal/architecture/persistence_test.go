package architecture

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistenceDoesNotDeclareForeignKeys(t *testing.T) {
	root := filepath.Join("..", "infrastructure", "persistence")
	forbidden := []string{"foreignkey", "foreign key", "references:", "constraint:on", "createconstraint"}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lower := strings.ToLower(string(contents))
		lower = strings.ReplaceAll(lower, "disableforeignkeyconstraintwhenmigrating", "")
		for _, token := range forbidden {
			if strings.Contains(lower, token) {
				t.Errorf("%s contains forbidden database relationship token %q", filepath.ToSlash(path), token)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk persistence packages: %v", err)
	}
}
