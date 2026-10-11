package quota

import (
	"sync"
	"time"
)

// Cache holds in-memory quota snapshots per AuthID.
type Cache struct {
	mu    sync.RWMutex
	items map[string]*AccountQuotaInfo
}

// NewCache creates a new thread-safe Cache.
func NewCache() *Cache {
	return &Cache{
		items: make(map[string]*AccountQuotaInfo),
	}
}

// Get returns the quota info for the given authID.
func (c *Cache) Get(authID string) (*AccountQuotaInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	info, ok := c.items[authID]
	if !ok {
		return nil, false
	}
	copy := *info
	return &copy, true
}

// Set stores or updates quota info for the authID.
func (c *Cache) Set(info *AccountQuotaInfo) {
	if info == nil || info.AuthID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	copy := *info
	c.items[info.AuthID] = &copy
}

// GetAll returns a snapshot slice of all cached accounts.
func (c *Cache) GetAll() []*AccountQuotaInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*AccountQuotaInfo, 0, len(c.items))
	for _, v := range c.items {
		copy := *v
		out = append(out, &copy)
	}
	return out
}

// UpdateFromCodexHeaders updates an account from HTTP response headers.
func (c *Cache) UpdateFromCodexHeaders(authID string, headers map[string][]string, now time.Time) {
	parsed, ok := ParseCodexHeaders(headers, now)
	if !ok {
		return
	}
	parsed.AuthID = authID
	c.Set(parsed)
}
