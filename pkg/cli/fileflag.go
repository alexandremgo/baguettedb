package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

// readFileFlag returns the content a --file flag names: the file, or stdin when file is -. what names the content in
// messages. No file, and content that is only blank, are usage errors.
func readFileFlag(stdin io.Reader, file, what string) ([]byte, error) {
	var content []byte
	var err error
	switch file {
	case "":
		return nil, usageError{err: fmt.Errorf("give the %s with --file, or --file - to read it from stdin", what)}
	case "-":
		content, err = io.ReadAll(stdin)
	default:
		content, err = os.ReadFile(file)
	}
	if err != nil {
		return nil, fmt.Errorf("reading the %s: %w", what, err)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return nil, usageError{err: errors.New("the " + what + " is empty")}
	}
	return content, nil
}
