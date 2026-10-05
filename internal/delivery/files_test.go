//go:build darwin || linux

package delivery

import (
	"context"
	"github.com/TheEditor/volley/internal/contract"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsafeTargets(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "config.toml")
	os.WriteFile(source, []byte("preserve\n"), 0600)
	for _, kind := range []string{"symlink", "fifo", "protected-hardlink"} {
		target := filepath.Join(root, kind)
		switch kind {
		case "symlink":
			os.Symlink(source, target)
		case "fifo":
			unix.Mkfifo(target, 0600)
		case "protected-hardlink":
			os.Link(source, target)
		}
		_, err := File(context.Background(), target, []byte("replace\n"), true, []string{source})
		e, ok := err.(*contract.Error)
		if !ok || e.Code != "OUTPUT_CONFLICT" {
			t.Fatal(kind, err)
		}
	}
	b, _ := os.ReadFile(source)
	if string(b) != "preserve\n" {
		t.Fatal("protected file changed")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 4 {
		t.Fatal("candidate retained", entries)
	}
}
func TestCanceledDelivery(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := File(ctx, filepath.Join(root, "out"), []byte("data"), false, nil); err == nil {
		t.Fatal("canceled delivery succeeded")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatal(entries)
	}
}
