package maintenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportKeepsDestinationOutsideSourceStorage(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	storage := filepath.Join(root, "storage")
	for _, dir := range []string{data, storage} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(storage, filepath.Join(data, "storage")); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(data, alias); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{
		data, filepath.Join(data, "output"), filepath.Join(alias, "output"), filepath.Join(storage, "output"),
	} {
		if _, err := prepareExportDirectory(data, target); err == nil ||
			!strings.Contains(err.Error(), "inside the source data") {
			t.Errorf("destination %q should be rejected, got %v", target, err)
		}
	}
	output := filepath.Join(root, "output")
	if _, err := prepareExportDirectory(data, output); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(output); err != nil || !info.IsDir() {
		t.Fatalf("sibling output directory not created: %v", err)
	}
}
