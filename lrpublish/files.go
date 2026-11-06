package lrpublish

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func tempPath(dest string) string {
	// Obsidian ignores dot-prefixed files.
	return filepath.Join(filepath.Dir(dest), "."+filepath.Base(dest)+".lr-publish")
}

// replace calls write to create a temporary file beside dest, then renames
// it over dest. On failure, it removes the temporary file.
func replace(dest string, write func(tmp string) error) error {
	tmp := tempPath(dest)
	os.Remove(tmp)
	if err := write(tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("cannot move %s to %s: %s", tmp, dest, describe(err))
	}
	return nil
}

func writeFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o666); err != nil {
		return fmt.Errorf("cannot write %s: %s", path, describe(err))
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("cannot read %s: %s", src, describe(err))
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return fmt.Errorf("cannot write %s: %s", dst, describe(err))
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("cannot write %s: %s", dst, describe(err))
	}
	return nil
}

// readFile returns a regular file's contents. A failed stat or non-regular
// file returns (nil, false, nil); a failed read returns an error.
func readFile(path string) ([]byte, bool, error) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("cannot read %s: %s", path, describe(err))
	}
	return data, true, nil
}

// readFrontMatter reads through the closing --- line, or only the first
// line if it is not ---. It stops after a line takes the total over 64 KiB.
func readFrontMatter(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var lines []string
	size := 0
	for {
		line, err := r.ReadString('\n')
		if line == "" && err != nil {
			if err != io.EOF {
				return "", err
			}
			break
		}
		line = strings.TrimSuffix(line, "\n")
		lines = append(lines, line)
		size += len(line) + 1
		fence := strings.TrimSuffix(line, "\r") == "---"
		if (len(lines) == 1) != fence || size > 65536 || err != nil {
			break
		}
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// writeNote replaces old contents or creates a note when old is nil.
// It returns an error without replacing the note if a reread differs from
// old. rel is the vault-relative path for messages.
func writeNote(path string, old *string, contents, rel string) error {
	os.MkdirAll(filepath.Dir(path), 0o777)
	return replace(path, func(tmp string) error {
		if err := writeFile(tmp, []byte(contents)); err != nil {
			return err
		}
		now, ok, err := readFile(path)
		if err != nil || ok != (old != nil) || (ok && string(now) != *old) {
			return fmt.Errorf("%s changed during the publish, so it was left as it is. Publish again to retry.", rel)
		}
		return nil
	})
}
