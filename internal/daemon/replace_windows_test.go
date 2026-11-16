//go:build windows

package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func denyStateFileReplacement(t *testing.T, path string) windows.Handle {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestStateFileReplacementSurvivesConcurrentWindowsReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipts.json")
	if err := writeFileAtomic(path, []byte("old")); err != nil {
		t.Fatal(err)
	}
	h := denyStateFileReplacement(t, path)
	defer func() {
		if h != windows.InvalidHandle {
			_ = windows.CloseHandle(h)
		}
	}()
	done := make(chan error, 1)
	go func() { done <- writeFileAtomic(path, []byte("new")) }()
	select {
	case err := <-done:
		t.Fatalf("replacement ended while the reader denied deletion: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if err := windows.CloseHandle(h); err != nil {
		t.Fatal(err)
	}
	h = windows.InvalidHandle
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement hung after reader closed")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestStateFileReplacementBoundsWindowsSharingFailures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "receipts.json")
	if err := writeFileAtomic(path, []byte("old")); err != nil {
		t.Fatal(err)
	}
	h := denyStateFileReplacement(t, path)
	defer windows.CloseHandle(h)
	start := time.Now()
	if err := writeFileAtomic(path, []byte("new")); err == nil {
		t.Fatal("locked target was replaced")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("sharing retry exceeded its bound")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "old" {
		t.Fatalf("original changed: %q %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files leaked: %v %v", entries, err)
	}
}
