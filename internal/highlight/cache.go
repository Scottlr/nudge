package highlight

import "sync"

// CacheKey identifies immutable highlighting input. Mutable repository paths
// are intentionally absent so renames and captures do not corrupt reuse.
type CacheKey struct {
	ContentHash string
	Lexer       string
	SyntaxStyle string
}

type cacheEntry struct {
	file  HighlightedFile
	bytes int
	used  uint64
}

// Cache is a bounded, concurrency-safe cache of immutable highlight results.
// The mutex protects the entry map, resident-byte accounting, and LRU clock;
// no external work occurs while it is held.
type Cache struct {
	mu         sync.Mutex
	maxEntries int
	maxBytes   int
	resident   int
	clock      uint64
	entries    map[CacheKey]cacheEntry
}

// NewCache creates a cache with explicit count and resident-byte bounds.
func NewCache(maxEntries, maxBytes int) *Cache {
	if maxEntries < 0 {
		maxEntries = 0
	}
	if maxBytes < 0 {
		maxBytes = 0
	}
	return &Cache{maxEntries: maxEntries, maxBytes: maxBytes, entries: make(map[CacheKey]cacheEntry)}
}

// Get returns a defensive copy of a cached result.
func (c *Cache) Get(key CacheKey) (HighlightedFile, bool) {
	if c == nil {
		return HighlightedFile{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return HighlightedFile{}, false
	}
	c.clock++
	entry.used = c.clock
	c.entries[key] = entry
	return cloneHighlightedFile(entry.file), true
}

// Put stores a result if it fits the configured bounds. Oversized entries are
// deliberately not retained, while still remaining valid return values to the
// current caller.
func (c *Cache) Put(key CacheKey, file HighlightedFile) {
	if c == nil || c.maxEntries == 0 || c.maxBytes == 0 {
		return
	}
	copyFile := cloneHighlightedFile(file)
	entryBytes := highlightedBytes(copyFile)
	if entryBytes > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[key]; ok {
		c.resident -= old.bytes
	}
	c.clock++
	c.entries[key] = cacheEntry{file: copyFile, bytes: entryBytes, used: c.clock}
	c.resident += entryBytes
	for len(c.entries) > c.maxEntries || c.resident > c.maxBytes {
		c.evictOldest()
	}
}

// Len returns the number of retained results.
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *Cache) evictOldest() {
	var oldest CacheKey
	var oldestUsed uint64
	first := true
	for key, entry := range c.entries {
		if first || entry.used < oldestUsed {
			oldest, oldestUsed, first = key, entry.used, false
		}
	}
	if first {
		return
	}
	c.resident -= c.entries[oldest].bytes
	delete(c.entries, oldest)
}

func highlightedBytes(file HighlightedFile) int {
	bytes := len(file.ContentHash) + len(file.Lexer) + len(file.SyntaxStyle) + len(file.LimitReason)
	for _, line := range file.Lines {
		for _, span := range line {
			bytes += len(span.Text) + len(span.Token)
		}
	}
	return bytes
}

func cloneHighlightedFile(file HighlightedFile) HighlightedFile {
	file.Lines = make([][]StyledSpan, len(file.Lines))
	for i, line := range file.Lines {
		file.Lines[i] = append([]StyledSpan(nil), line...)
	}
	return file
}
