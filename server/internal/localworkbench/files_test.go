package localworkbench

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileStorePutOpenAndRemove(t *testing.T) {
	t.Parallel()
	store, err := OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenFileStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.Put(t.Context(), filepath.Join("attachments", "item.txt"), bytes.NewBufferString("hello")); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	file, err := store.Open(filepath.Join("attachments", "item.txt"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	content, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("content = %q, want hello", content)
	}
	if err := store.Remove(filepath.Join("attachments", "item.txt")); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
}

func TestFileStoreRejectsTraversalAndPartialWrites(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := OpenFileStore(root)
	if err != nil {
		t.Fatalf("OpenFileStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.Put(t.Context(), "../outside", bytes.NewBufferString("no")); err == nil {
		t.Fatal("Put(traversal) error = nil, want rejection")
	}
	wantErr := errors.New("source failed")
	if err := store.Put(t.Context(), "partial.bin", failingReader{err: wantErr}); !errors.Is(err, wantErr) {
		t.Fatalf("Put(partial) error = %v, want wrapped %v", err, wantErr)
	}
	if _, err := store.Open("partial.bin"); err == nil {
		t.Fatal("Open(partial.bin) error = nil, want missing file")
	}
}

func TestBoundedAttachmentReaderRejectsTheFirstByteOverLimit(t *testing.T) {
	reader := &boundedAttachmentReader{reader: strings.NewReader("12345"), remaining: 4}
	data, err := io.ReadAll(reader)
	if !errors.Is(err, errAttachmentTooLarge) {
		t.Fatalf("error=%v, want errAttachmentTooLarge", err)
	}
	if string(data) != "1234" {
		t.Fatalf("data=%q, want first four bytes", data)
	}
}

type failingReader struct {
	err error
}

func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}
