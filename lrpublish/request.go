package lrpublish

import "strings"

// Protocol is the version of the request and response format that the
// command implements: ../vaultphotos/PROTOCOL.md.
const Protocol = 5

// Request is a plugin operation. Op selects the required fields:
//
//	check    none
//	publish  Service, and Collection with Photos, or neither
//	delete   Collection, RemoteIDs
type Request struct {
	Protocol   int          `json:"protocol"`
	Op         string       `json:"op"`
	Service    []Collection `json:"service,omitempty"`
	Collection int64        `json:"collection,omitempty"`
	Photos     []Photo      `json:"photos,omitempty"`
	RemoteIDs  []string     `json:"remoteIds,omitempty"`
}

// A Collection is a published collection of the service.
type Collection struct {
	ID int64 `json:"id"`
	// Path holds the names of the collection's enclosing collection sets,
	// outermost first, then its own name.
	Path []string `json:"path,omitempty"`
}

func (c Collection) name() string {
	if len(c.Path) == 0 {
		return ""
	}
	return c.Path[len(c.Path)-1]
}

// path returns the collection's lightroom-path: its sets' names and its
// own, joined with /.
func (c Collection) path() string {
	return strings.Join(c.Path, "/")
}

// A Photo is a rendered photo to store.
type Photo struct {
	UUID      string `json:"uuid"`
	Rendition string `json:"rendition"` // the rendered full-size file
	Caption   string `json:"caption,omitempty"`
}

// Response reports an operation's results, errors, and warnings.
type Response struct {
	Protocol int `json:"protocol"`
	// Error describes an operation-wide failure; empty means no such failure.
	Error     string        `json:"error,omitempty"`
	Warnings  []Warning     `json:"warnings,omitempty"`
	Photos    []PhotoResult `json:"photos,omitempty"`    // publish
	Confirmed []string      `json:"confirmed,omitempty"` // delete: the remote IDs removed
}

// PhotoResult identifies a photo by UUID and supplies either RemoteID
// on success or Error on failure.
type PhotoResult struct {
	UUID     string `json:"uuid"`
	RemoteID string `json:"remoteId,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Warning is a nonfatal problem for the user. Title includes any service prefix.
type Warning struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}
