package cachefile

import (
	"os"

	"github.com/sagernet/bbolt"
	"github.com/sagernet/sing-box/adapter"
)

func (c *CacheFile) LoadSubscription(tag string) *adapter.SavedSubscription {
	var saved adapter.SavedSubscription
	c.pendingAccess.RLock()
	data, found := c.pending.subscriptions[tag]
	if !found && c.writing != nil {
		data, found = c.writing.subscriptions[tag]
	}
	if found {
		err := saved.UnmarshalBinary(data)
		c.pendingAccess.RUnlock()
		if err != nil {
			return nil
		}
		return &saved
	}
	c.pendingAccess.RUnlock()
	err := c.view(func(tx *bbolt.Tx) error {
		bucket := c.bucket(tx, bucketSubscription)
		if bucket == nil {
			return os.ErrNotExist
		}
		return saved.UnmarshalBinary(bucket.Get([]byte(tag)))
	})
	if err != nil {
		return nil
	}
	return &saved
}

func (c *CacheFile) SaveSubscription(tag string, subscription *adapter.SavedSubscription) error {
	data, err := subscription.MarshalBinary()
	if err != nil {
		return err
	}
	c.pendingAccess.Lock()
	previous, exists := c.pending.subscriptions[tag]
	c.pending.subscriptions[tag] = data
	delta := len(data) - len(previous)
	if !exists {
		delta += len(tag)
	}
	c.enqueueLocked(!exists, delta)
	c.pendingAccess.Unlock()
	return nil
}
