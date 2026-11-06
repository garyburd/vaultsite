package lrpublish

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/garyburd/vaultsite/site"
)

// Check reports service name's configuration, paths, and collection-note
// count to w. dir is the vault or a directory within it. Configuration
// errors and a missing notes folder return user-facing errors.
func Check(dir, name string, w io.Writer) error {
	s, err := loadService(dir, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "vault:      %s\n", s.cfg.VaultRoot)
	fmt.Fprintf(w, "site.yaml:  %s\n", s.file)
	fmt.Fprintf(w, "service:    %s\n", s.name)
	missing := !isDir(s.abs(s.Notes))
	state := "exists"
	if missing {
		state = "missing"
	}
	fmt.Fprintf(w, "%-11s %s (%s)\n", "notes:", s.Notes, state)
	fmt.Fprintf(w, "%-11s %s\n", "thumbnails:", s.thumbnails())
	fmt.Fprintf(w, "%-11s %s\n", "full size:", filepath.Join(s.cfg.Dir, site.FullsizeDir, s.name))
	n := 0
	for _, rels := range s.scanNotes().byID {
		n += len(rels)
	}
	fmt.Fprintf(w, "notes with lightroom-collection: %d\n", n)
	if s.Template == "" {
		fmt.Fprintln(w, "template:   none")
	} else {
		fmt.Fprintln(w, "template:")
		for line := range strings.SplitSeq(strings.TrimSuffix(s.Template, "\n"), "\n") {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
	if missing {
		return fmt.Errorf("create the notes folder: the service never creates it")
	}
	return nil
}
