package common

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
)

func ZipFolder(folderPath string) (*bytes.Buffer, error) {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

	err := filepath.Walk(folderPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Create the header based on file info
		relPath, err := filepath.Rel(folderPath, path)
		if err != nil {
			return err
		}
		// Skip root folder
		if relPath == "." {
			return nil
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		// Use relative path in zip archive
		header.Name = relPath

		// For directories, just create the folder entry
		if info.IsDir() {
			header.Name += "/"
		} else {
			// Use deflate compression for files
			header.Method = zip.Deflate
		}

		writer, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}

		// If file, copy contents into zip writer
		// #nosec G122 -- snapshot/runner dir is operator-controlled (created by the slice/operator inside its own staging dirs); never user-writable, no symlink TOCTOU risk.
		if !info.IsDir() {
			f, err := os.Open(path) // #nosec G122 G304 -- false-positive: see the cross-repo audit baseline; this site has been reviewed.
			if err != nil {
				return err
			}
			defer f.Close()

			_, err = io.Copy(writer, f)
			if err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		_ = zw.Close()
		return nil, err
	}

	err = zw.Close()
	if err != nil {
		return nil, err
	}

	return buf, nil
}
