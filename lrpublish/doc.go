// Package lrpublish stores photos and updates collection notes for the
// Vault Photos Lightroom plugin. Run handles check, publish, and delete
// requests; Check reports a service's configuration for terminal use.
// Services are configured under lr_publish in site.yaml. The wire protocol
// is defined in ../vaultphotos/PROTOCOL.md.
//
// Each collection has a Markdown note identified by lightroom-collection
// in its front matter. Publishing stores full-size images and thumbnails,
// then appends figures for photos not already shown. Existing captions and
// other user text are preserved. Removing a photo deletes its figures and
// captions, but keeps its image files.
//
// Every publish synchronizes collection paths and marks notes of deleted
// collections as drafts. A publish without a collection only synchronizes
// notes. Notes and thumbnails may be moved: notes are found by collection
// ID, and thumbnails by their figure links. The configured notes folder must
// already exist.
//
// Note conflicts fail the photos whose figures could not be added, allowing
// the next publish to retry. Image files may already have been written.
// Calls for the same service must not overlap.
package lrpublish
