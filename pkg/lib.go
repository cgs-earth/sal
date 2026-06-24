package pkg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrSalDirNotFound = errors.New("sal directory not found")
var ErrCantMakeSalDirInHome = errors.New("a sal project directory should not be the home directory; ~/.sal is intended for user-wide configuration")

// FindSALProjectDir walks up from the current directory to find the nearest
// project-local .sal directory without crossing the user's home directory.
func FindSALProjectDir(getHomeDir func() (string, error)) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}

	home, err := getHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine user home directory: %w", err)
	}
	cwd, err = canonicalPath(cwd)
	if err != nil {
		return "", fmt.Errorf("failed to resolve current directory: %w", err)
	}
	home, err = canonicalPath(home)
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}

	for {
		if cwd == home {
			return "", ErrCantMakeSalDirInHome
		}

		salDir := filepath.Join(cwd, ".sal")
		if info, err := os.Stat(salDir); err == nil && info.IsDir() {
			return cwd, nil
		}

		parent := filepath.Dir(cwd)
		if parent == cwd {
			break
		}

		cwd = parent
	}

	return "", ErrSalDirNotFound
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}
