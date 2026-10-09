package serve

import (
	"container/list"
	"errors"
	"sync"
)

// Immutable variant URLs allow caching across builds. The LRU bounds encoded
// bytes; joining concurrent requests avoids decoding the same original twice.
type variantCache struct {
	max int64

	mu    sync.Mutex
	size  int64
	order *list.List // most recently used first; values are *cacheEntry
	items map[string]*list.Element
	calls map[string]*call
}

type cacheEntry struct {
	url  string
	data []byte
}

// call is a generation in progress.
type call struct {
	done chan struct{}
	data []byte
	err  error
}

func newVariantCache(max int64) *variantCache {
	return &variantCache{max: max, order: list.New(), items: make(map[string]*list.Element), calls: make(map[string]*call)}
}

// get joins concurrent calls to generate. Errors reach all waiters but are not cached.
func (c *variantCache) get(url string, generate func() ([]byte, error)) ([]byte, error) {
	c.mu.Lock()
	if e, ok := c.items[url]; ok {
		c.order.MoveToFront(e)
		data := e.Value.(*cacheEntry).data
		c.mu.Unlock()
		return data, nil
	}
	if cl, ok := c.calls[url]; ok {
		c.mu.Unlock()
		<-cl.done
		return cl.data, cl.err
	}
	cl := &call{done: make(chan struct{})}
	c.calls[url] = cl
	c.mu.Unlock()

	// Release waiters even if generate panics; net/http recovers the panic
	// in this goroutine, and the others would otherwise block forever.
	defer func() {
		c.mu.Lock()
		delete(c.calls, url)
		// A variant larger than the whole cache is served but not kept.
		if cl.err == nil && cl.data != nil && int64(len(cl.data)) <= c.max {
			c.items[url] = c.order.PushFront(&cacheEntry{url, cl.data})
			c.size += int64(len(cl.data))
			for c.size > c.max {
				last := c.order.Back()
				e := c.order.Remove(last).(*cacheEntry)
				delete(c.items, e.url)
				c.size -= int64(len(e.data))
			}
		}
		c.mu.Unlock()
		close(cl.done)
	}()
	cl.err = errors.New("variant generation panicked")
	cl.data, cl.err = generate()
	return cl.data, cl.err
}
