package lrpublish

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Title is the Lightroom service name and warning-title prefix.
const Title = "Vault Photos"

// Run handles req for service name in the vault containing dir and writes
// trace lines to logw. An unknown operation returns an error; configuration
// and operation failures appear in the response. The caller must validate
// req.Protocol before calling Run.
func Run(dir, name string, req *Request, logw io.Writer) (*Response, error) {
	o := &op{req: req, resp: &Response{Protocol: Protocol}, log: log.New(logw, "", 0)}
	run := map[string]func() error{
		"check":   o.checkFolders,
		"publish": o.publish,
		"delete":  o.delete,
	}[req.Op]
	if run == nil {
		return nil, fmt.Errorf("unknown op %q", req.Op)
	}
	s, err := loadService(dir, name)
	if err == nil {
		o.s = s
		err = run()
	}
	if err != nil {
		o.resp.Error = err.Error()
	}
	return o.resp, nil
}

type op struct {
	req  *Request
	resp *Response
	log  *log.Logger
	s    *service
}

func (o *op) warn(title, message string) {
	o.resp.Warnings = append(o.resp.Warnings, Warning{Title: Title + ": " + title, Message: message})
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

//
// Publishing
//

// target holds the collection and note snapshot used by a publish.
type target struct {
	service    map[int64]Collection // the service's collections, by ID
	collection Collection
	notes      *notes
	rel        string // the collection's note, or ""
	contents   string // the note's contents
	note       *note  // the note, or nil
}

// begin runs the checks that stop a publish: a missing notes folder, and
// duplicate or unreadable notes.
func (o *op) begin() (*target, error) {
	s := o.s
	t := &target{service: serviceMap(o.req.Service)}
	c, ok := t.service[o.req.Collection]
	if !ok || len(c.Path) == 0 {
		return nil, fmt.Errorf("collection %d is not in the service", o.req.Collection)
	}
	t.collection = c
	if err := o.checkFolders(); err != nil {
		return nil, err
	}

	// Thumbnails are written where the collection's figures show them, so
	// the note must be readable.
	t.notes = s.scanNotes()
	rel, err := t.notes.noteOf(c)
	if err != nil {
		return nil, err
	}
	if rel != "" {
		t.rel = rel
		t.contents, err = s.readNote(rel)
		if err != nil {
			return nil, fmt.Errorf("%s: %v\n\nFix the note, then publish again.", rel, err)
		}
		t.note = s.loadNote(t.contents)
	}
	return t, nil
}

func (o *op) checkFolders() error {
	// Do not recreate a moved folder before the user updates site.yaml.
	s := o.s
	if !isDir(s.abs(s.Notes)) {
		return fmt.Errorf("The notes folder %s is missing.\n\nCreate it, or if you moved it, change %s in %s "+
			"to its new place.", s.Notes, s.setting("notes"), s.file)
	}
	return nil
}

// A stored photo is one whose images a publish wrote.
type stored struct {
	id      string
	thumb   string // the thumbnail's vault path
	caption string
	added   bool // updateNote added a figure for it
	result  int  // the index of its result in the response
}

// publish stores photos, updates their note, and synchronizes collections.
// Without a collection, it only synchronizes. Note failures affect photos
// needing a new figure; existing figures need no note update.
func (o *op) publish() error {
	if o.req.Service == nil {
		return errors.New("the request has no service")
	}
	if o.req.Collection == 0 {
		if len(o.req.Photos) > 0 {
			return errors.New("the request has photos but no collection")
		}
		if err := o.checkFolders(); err != nil {
			return err
		}
		o.sync(o.s.scanNotes(), serviceMap(o.req.Service))
		return nil
	}
	t, err := o.begin()
	if err != nil {
		return err
	}
	var photos []stored
	for _, p := range o.req.Photos {
		st, ext, err := o.store(p, t.note)
		if err != nil {
			o.resp.Photos = append(o.resp.Photos, PhotoResult{UUID: p.UUID, Error: err.Error()})
			continue
		}
		st.result = len(o.resp.Photos)
		o.resp.Photos = append(o.resp.Photos, PhotoResult{UUID: p.UUID, RemoteID: remoteID(st.id, ext)})
		photos = append(photos, st)
	}
	o.log.Printf("%s: stored %d of %d photos", t.collection.name(), len(photos), len(o.req.Photos))

	if err := o.updateNote(t, photos); err != nil {
		failed := 0
		for _, p := range photos {
			if p.added {
				o.resp.Photos[p.result] = PhotoResult{UUID: o.resp.Photos[p.result].UUID, Error: err.Error()}
				failed++
			}
		}
		if failed == 0 {
			o.warn("the note was not changed", err.Error())
		}
	}
	o.sync(t.notes, t.service)
	return nil
}

func serviceMap(service []Collection) map[int64]Collection {
	m := make(map[int64]Collection, len(service))
	for _, c := range service {
		m[c.ID] = c
	}
	return m
}

// sync marks deleted collections' notes and updates surviving collections'
// lightroom-path values.
func (o *op) sync(notes *notes, service map[int64]Collection) {
	var deleted []string
	for id, rels := range notes.byID {
		if _, ok := service[id]; !ok {
			deleted = append(deleted, rels...)
		}
	}
	o.markDeleted(deleted)
	paths := map[int64]string{}
	for id, c := range service {
		paths[id] = c.path()
	}
	o.syncPaths(notes, paths)
}

// store writes the full-size image and thumbnail, using the first figure's
// thumbnail path or the default. It removes old full-size formats and skips
// unchanged thumbnails. An error may leave the full-size image updated.
func (o *op) store(p Photo, n *note) (stored, string, error) {
	s := o.s
	id, ok := normalizeID(p.UUID)
	if !ok {
		return stored{}, "", fmt.Errorf("bad photo UUID %s", p.UUID)
	}
	ext, ok := extension(p.Rendition)
	if !ok {
		return stored{}, "", fmt.Errorf("rendered file has no extension: %s", p.Rendition)
	}

	thumb, fromFigure := thumbPath(s.thumbnails(), id), false
	if n != nil {
		if f, ok := n.shows(id); ok {
			thumb, fromFigure = f.thumb, true
		}
	}
	thumbDest, err := o.thumbnailPath(thumb, fromFigure)
	if err != nil {
		return stored{}, "", err
	}
	data, err := makeThumbnail(p.Rendition)
	if err != nil {
		return stored{}, "", err
	}

	dest := s.fullsizePath(id, ext)
	os.MkdirAll(filepath.Dir(dest), 0o777)
	if err := replace(dest, func(tmp string) error { return copyFile(p.Rendition, tmp) }); err != nil {
		return stored{}, "", err
	}
	if err := removeOtherFormats(dest, id); err != nil {
		return stored{}, "", err
	}

	if old, ok, err := readFile(thumbDest); err == nil && ok && bytes.Equal(old, data) {
		o.log.Printf("thumbnail %s: unchanged", id)
	} else if err := replace(thumbDest, func(tmp string) error { return writeFile(tmp, data) }); err != nil {
		return stored{}, "", err
	}
	return stored{id: id, thumb: thumb, caption: p.Caption}, ext, nil
}

// removeOtherFormats deletes the full-size images of photo id beside
// dest, other than dest.
func removeOtherFormats(dest, id string) error {
	entries, _ := os.ReadDir(filepath.Dir(dest))
	for _, e := range entries {
		ext, ok := strings.CutPrefix(e.Name(), id+".")
		path := filepath.Join(filepath.Dir(dest), e.Name())
		if !ok || strings.Contains(ext, ".") || path == dest {
			continue
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("cannot delete %s: %s", path, describe(err))
		}
	}
	return nil
}

// thumbnailPath returns the absolute path of rel, a thumbnail's vault
// path from a figure or a default path. The default path's folders are
// created as needed, but a figure's folder, which the user may have moved,
// is never created.
func (o *op) thumbnailPath(rel string, fromFigure bool) (string, error) {
	if _, err := segments(rel); err != nil {
		return "", fmt.Errorf("a figure shows a bad thumbnail path: %s", rel)
	}
	path := o.s.abs(rel)
	parent := filepath.Dir(path)
	if !fromFigure {
		os.MkdirAll(parent, 0o777)
	} else if !isDir(parent) {
		return "", fmt.Errorf("the folder of the thumbnail %s is missing. If you moved it, "+
			"the figure should show the new place; if you deleted it, delete the figure.", rel)
	}
	return path, nil
}

// updateNote adds missing figures, creating the note if needed. It sets
// stored.added for each new figure, even if saving fails, so the caller can
// fail those photos for retry.
func (o *op) updateNote(t *target, photos []stored) error {
	if t.note == nil {
		return o.createNote(t, photos)
	}
	n, seen := t.note, map[string]bool{}
	added := 0
	for i := range photos {
		p := &photos[i]
		if seen[p.id] {
			continue
		}
		seen[p.id] = true
		if _, ok := n.shows(p.id); ok {
			continue
		}
		n.add(o.s.figureParagraph(p.id, p.thumb, p.caption))
		p.added = true
		added++
	}
	updated := n.save()
	if updated == t.contents {
		return nil
	}
	if err := writeNote(o.s.abs(t.rel), &t.contents, updated, t.rel); err != nil {
		return err
	}
	o.log.Printf("%s: added %d figure%s", t.rel, added, plural(added))
	return nil
}

// createNote creates a note containing only the photos in this publish.
func (o *op) createNote(t *target, photos []stored) error {
	s, c := o.s, t.collection

	sets, name := c.Path[:len(c.Path)-1], c.name()
	rel, i := newNotePath(s.Notes, sets, name, 1), 1
	if isUnder(rel, s.thumbnails()) {
		return fmt.Errorf("The note of \"%s\" would be %s, inside the thumbnail folder. "+
			"Rename its collection set.", name, rel)
	}
	for exists(s.abs(rel)) {
		i++
		rel = newNotePath(s.Notes, sets, name, i)
	}
	n := s.loadNote(newFrontMatter(s.Template, c.ID, c.path()))
	seen := map[string]bool{}
	for i := range photos {
		p := &photos[i]
		if !seen[p.id] {
			seen[p.id] = true
			n.add(s.figureParagraph(p.id, p.thumb, p.caption))
			p.added = true
		}
	}
	if err := writeNote(s.abs(rel), nil, n.save(), rel); err != nil {
		return err
	}
	o.log.Printf("created %s with %d photo%s", rel, len(seen), plural(len(seen)))
	return nil
}

// syncPaths updates lightroom-path values, skipping duplicate claims.
// Failures are combined into one warning and retried by the next publish.
func (o *op) syncPaths(notes *notes, paths map[int64]string) {
	s := o.s
	ids := make([]int64, 0, len(paths))
	for id := range paths {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var failed []string
	changed := 0
	for _, id := range ids {
		path, rels := paths[id], notes.byID[id]
		if len(rels) != 1 {
			continue
		}
		rel := rels[0]
		if old, ok := notes.pathOf[rel]; ok && old == path {
			continue
		}
		contents, err := s.readNote(rel)
		if err != nil {
			failed = append(failed, err.Error())
			continue
		}
		updated, ok := setCollectionPath(contents, path)
		if !ok || updated == contents {
			continue
		}
		if err := writeNote(s.abs(rel), &contents, updated, rel); err != nil {
			failed = append(failed, err.Error())
			continue
		}
		changed++
	}
	if len(failed) > 0 {
		slices.Sort(failed)
		o.warn("collection paths were not updated", strings.Join(failed, "\n\n"))
	}
	if changed > 0 {
		o.log.Printf("updated the collection path of %d note%s", changed, plural(changed))
	}
}

//
// Other operations
//

// delete removes figures and confirms remote IDs only after saving.
// Missing notes or figures are already deleted; failed saves are retried.
func (o *op) delete() error {
	s := o.s
	rels := s.scanNotes().byID[o.req.Collection]
	const title = "figures of removed photos were not deleted"
	switch len(rels) {
	case 0:
		o.resp.Confirmed = o.req.RemoteIDs
		return nil
	case 1:
	default:
		o.warn(title, "Several notes claim the collection:\n\n"+strings.Join(rels, "\n")+
			"\n\nRemove the lightroom-collection line from all but one, then remove the photos again.")
		return nil
	}
	rel := rels[0]
	contents, err := s.readNote(rel)
	if err != nil {
		o.warn(title, rel+": "+err.Error())
		return nil
	}
	n := s.loadNote(contents)
	deleted := 0
	for _, remoteID := range o.req.RemoteIDs {
		if id, _, ok := parseRemoteID(remoteID); ok {
			deleted += n.remove(id)
		}
	}
	if updated := n.save(); updated != contents {
		if err := writeNote(s.abs(rel), &contents, updated, rel); err != nil {
			o.warn(title, err.Error())
			return nil
		}
	}
	o.log.Printf("%s: deleted %d figure%s of %d removed photo%s", rel, deleted, plural(deleted),
		len(o.req.RemoteIDs), plural(len(o.req.RemoteIDs)))
	o.resp.Confirmed = o.req.RemoteIDs
	return nil
}

// markDeleted detaches the notes at rels and marks them as drafts.
// It keeps image files because other notes may reference them.
func (o *op) markDeleted(rels []string) {
	slices.Sort(rels)
	var problems []string
	for _, rel := range rels {
		contents, err := o.s.readNote(rel)
		if err == nil {
			if updated, ok := markDeleted(contents); ok {
				err = writeNote(o.s.abs(rel), &contents, updated, rel)
			}
		}
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		o.log.Printf("marked %s deleted", rel)
	}
	if len(problems) > 0 {
		o.warn("notes of deleted collections were not marked", strings.Join(problems, "\n\n")+
			"\n\nPublish again to retry, or remove their lightroom-collection lines yourself.")
	}
}
