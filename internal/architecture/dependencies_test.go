package architecture

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLayerDependencies(t *testing.T) {
	forbidden := map[string][]string{
		"domain":         {"/internal/application/", "/internal/interfaces/", "/internal/infrastructure/", "/internal/di/"},
		"application":    {"/internal/interfaces/", "/internal/infrastructure/", "/internal/di/"},
		"interfaces":     {"/internal/infrastructure/", "/internal/di/"},
		"infrastructure": {"/internal/interfaces/", "/internal/di/"},
	}
	internalRoot := filepath.Join("..", "..", "internal")
	err := filepath.WalkDir(internalRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		relative, err := filepath.Rel(internalRoot, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if len(parts) == 0 {
			return nil
		}
		denied, guarded := forbidden[parts[0]]
		if !guarded {
			return nil
		}

		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			for _, fragment := range denied {
				if strings.Contains("/"+imported+"/", fragment) {
					t.Errorf("%s imports forbidden package %s", filepath.ToSlash(relative), imported)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal packages: %v", err)
	}
}
