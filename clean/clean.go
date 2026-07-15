package clean

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cgs-earth/sal/pkg"
)

type CleanCmd struct {
}

func (cmd *CleanCmd) Run() error {
	dataProductPath, err := pkg.SalBuiltDataProductPath()
	if err != nil {
		return err
	}

	var totalBytes int64

	err = filepath.WalkDir(dataProductPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		totalBytes += info.Size()
		return nil
	})

	if errors.Is(err, os.ErrNotExist) {
		slog.Warn("No data product to clean at " + dataProductPath)
		return nil
	}
	if err != nil {
		return err
	}

	if err := os.RemoveAll(dataProductPath); err != nil {
		return err
	}

	msg := fmt.Sprintf("Removed %s of data artifacts from %s", pkg.BytesToHumanReadable(totalBytes), dataProductPath)

	slog.Info(msg)

	return nil
}
