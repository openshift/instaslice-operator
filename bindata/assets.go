package bindata

import (
	"embed"
	"io/fs"
	"path/filepath"
)

//go:embed assets/*
var f embed.FS

// Asset reads and returns the content of the named file.
func Asset(name string) ([]byte, error) {
	return f.ReadFile(name)
}

// MustAsset reads and returns the content of the named file or panics
// if something went wrong.
func MustAsset(name string) []byte {
	data, err := f.ReadFile(name)
	if err != nil {
		panic(err)
	}

	return data
}

// AssetDir returns the file names in the given directory within the embedded filesystem.
func AssetDir(dir string) ([]string, error) {
	entries, err := fs.ReadDir(f, dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, filepath.Base(entry.Name()))
		}
	}
	return names, nil
}
