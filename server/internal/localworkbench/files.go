package localworkbench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

type FileStore struct {
	root *os.Root
}

func OpenFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, errors.New("file storage path is empty")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, fmt.Errorf("create file storage root: %w", err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open file storage root: %w", err)
	}
	return &FileStore{root: root}, nil
}

func (s *FileStore) Close() error {
	return s.root.Close()
}

func (s *FileStore) Put(ctx context.Context, name string, source io.Reader) (err error) {
	name, err = localFileName(name)
	if err != nil {
		return err
	}
	parent := filepath.Dir(name)
	if parent != "." {
		if err := s.root.MkdirAll(parent, 0o700); err != nil {
			return fmt.Errorf("create file parent: %w", err)
		}
	}
	temporary := filepath.Join(parent, "."+filepath.Base(name)+"."+uuid.NewString()+".tmp")
	file, err := s.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			closeErr := file.Close()
			if closeErr != nil && err == nil {
				err = fmt.Errorf("close temporary file: %w", closeErr)
			}
		}
		if err != nil {
			_ = s.root.Remove(temporary)
		}
	}()

	if _, err := io.Copy(file, contextReader{ctx: ctx, reader: source}); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file before rename: %w", err)
	}
	closed = true
	if err := s.root.Rename(temporary, name); err != nil {
		return fmt.Errorf("commit file: %w", err)
	}
	return nil
}

func (s *FileStore) Open(name string) (*os.File, error) {
	name, err := localFileName(name)
	if err != nil {
		return nil, err
	}
	file, err := s.root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open stored file: %w", err)
	}
	return file, nil
}

func (s *FileStore) Remove(name string) error {
	name, err := localFileName(name)
	if err != nil {
		return err
	}
	if err := s.root.Remove(name); err != nil {
		return fmt.Errorf("remove stored file: %w", err)
	}
	return nil
}

func (s *FileStore) Rename(oldName, newName string) error {
	oldName, err := localFileName(oldName)
	if err != nil {
		return err
	}
	newName, err = localFileName(newName)
	if err != nil {
		return err
	}
	parent := filepath.Dir(newName)
	if parent != "." {
		if err := s.root.MkdirAll(parent, 0o700); err != nil {
			return fmt.Errorf("create renamed file parent: %w", err)
		}
	}
	if err := s.root.Rename(oldName, newName); err != nil {
		return fmt.Errorf("rename stored file: %w", err)
	}
	return nil
}

func localFileName(name string) (string, error) {
	if name == "" || name == "." || !filepath.IsLocal(name) {
		return "", errors.New("file identity must be a contained relative path")
	}
	return filepath.Clean(name), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
