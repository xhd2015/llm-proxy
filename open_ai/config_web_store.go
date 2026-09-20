package openai

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	logutil "github.com/xhd2015/llm-proxy/log"
)

// The editor accepts bounded regular files; auth files and arbitrary paths are not API inputs.
const configWebMaxBytes = 4 << 20

var errConfigWebChanged = errors.New("config changed on disk; reload before saving")

type configWebStore struct {
	path string
	mu   sync.Mutex
}

func newConfigWebStore(path string) (*configWebStore, error) {
	expanded, err := logutil.ExpandPath(path)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(expanded)
	if err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return nil, err
	}
	store := &configWebStore{path: absolute}
	if _, _, err := store.read(); err != nil {
		return nil, err
	}
	return store, nil
}

func configWebRevision(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func (s *configWebStore) readFile() ([]byte, os.FileInfo, error) {
	info, err := os.Lstat(s.path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("config must remain a regular file")
	}
	file, err := os.Open(s.path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, nil, errConfigWebChanged
	}
	data, err := io.ReadAll(io.LimitReader(file, configWebMaxBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > configWebMaxBytes {
		return nil, nil, fmt.Errorf("config exceeds the 4 MiB editor limit")
	}
	return data, opened, nil
}

func (s *configWebStore) read() (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, _, err := s.readFile()
	if err != nil {
		return "", "", err
	}
	return string(data), configWebRevision(data), nil
}

// save retains the submitted JSON bytes, not the normalized runtime struct.
// The mutex serializes editor saves; revision checks detect external edits before replacement.
func (s *configWebStore) save(text, revision string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, info, err := s.readFile()
	if err != nil {
		return "", "", err
	}
	if revision != configWebRevision(old) {
		return "", "", errConfigWebChanged
	}
	if text == string(old) {
		return revision, "", nil
	}
	backup, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".backup-*")
	if err != nil {
		return "", "", err
	}
	backupPath := backup.Name()
	_, err = backup.Write(old)
	if err == nil {
		err = backup.Sync()
	}
	closeErr := backup.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", "", fmt.Errorf("backup failed: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".llm-proxy-config-*")
	if err != nil {
		return "", "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.WriteString(text); err != nil {
		return "", "", err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		return "", "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		return "", "", err
	}
	current, currentInfo, err := s.readFile()
	if err != nil {
		return "", "", err
	}
	if configWebRevision(current) != revision || !os.SameFile(info, currentInfo) || info.Mode() != currentInfo.Mode() {
		return "", "", errConfigWebChanged
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return "", "", err
	}
	return configWebRevision([]byte(text)), backupPath, nil
}
