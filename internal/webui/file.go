package webui

import (
	"errors"
	"io"
	"os"
)

var errMissing = errors.New("missing")

func openReadable(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errMissing
		}
		return nil, err
	}
	return f, nil
}
