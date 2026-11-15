package manager

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestQueueWatchedFileDeduplicatesAndReschedulesChanges(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "video.mp4")
	fw := &fileWatcher{
		scanQueue:    make(chan string, 1),
		pendingFiles: make(map[string]bool),
	}
	manager := &Manager{}

	manager.queueWatchedFile(ctx, path, fw)
	if got := <-fw.scanQueue; got != path {
		t.Fatalf("queued path = %q, want %q", got, path)
	}

	manager.queueWatchedFile(ctx, path, fw)
	if !fw.completePendingFile(path) {
		t.Fatal("expected a change during the pending scan to request a rescan")
	}
	if fw.completePendingFile(path) {
		t.Fatal("expected the rescheduled scan to clear the pending path")
	}
}

func TestPathsForLibraryScan(t *testing.T) {
	tempDir := t.TempDir()
	libraryPath := filepath.Join(tempDir, "library")
	subdirectory := filepath.Join(libraryPath, "subdirectory")
	if err := os.MkdirAll(subdirectory, 0755); err != nil {
		t.Fatal(err)
	}
	videoPath := filepath.Join(subdirectory, "video.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		scanPaths []string
		want      []string
	}{
		{
			name: "default scan includes the whole library",
			want: []string{libraryPath},
		},
		{
			name:      "nested scan stays scoped to its subdirectory",
			scanPaths: []string{subdirectory},
			want:      []string{subdirectory},
		},
		{
			name:      "parent scan is narrowed to the library",
			scanPaths: []string{tempDir},
			want:      []string{libraryPath},
		},
		{
			name:      "file scan is narrowed to its directory",
			scanPaths: []string{videoPath},
			want:      []string{subdirectory},
		},
		{
			name:      "unrelated scan is ignored",
			scanPaths: []string{filepath.Join(tempDir, "other")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pathsForLibraryScan(tt.scanPaths, libraryPath)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("pathsForLibraryScan() = %v, want %v", got, tt.want)
			}
		})
	}
}
