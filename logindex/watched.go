package logindex

import (
	"sync"

	. "github.com/divtxt/raft"
)

// WatchedIndex is a LogIndex that implements WatchableIndex.
type WatchedIndex struct {
	lock      sync.Locker
	value     LogIndex
	listeners []IndexChangeListener
}

// NewWatchedIndex creates a new WatchedIndex that uses the given Locker
// to ensure safe concurrent access.
// The initial LogIndex value will be 0.
func NewWatchedIndex(lock sync.Locker) *WatchedIndex {
	return &WatchedIndex{
		lock:      lock,
		value:     0,
		listeners: nil,
	}
}

// Get the current value.
// This will use the underlying Locker to ensure safe concurrent access.
func (p *WatchedIndex) Get() LogIndex {
	p.lock.Lock()
	v := p.value
	p.lock.Unlock()
	return v
}

// UnsafeGet gets the current value WITHOUT locking the underlying Locker.
// The caller MUST have locked the underlying Locker to ensure safe concurrent operation.
func (p *WatchedIndex) UnsafeGet() LogIndex {
	return p.value
}

// Add the given callback as a listener for changes.
// This will use the underlying Locker to ensure safe concurrent access.
//
// Whenever the underlying value changes, all listeners will be called in order.
// Any listener can indicate an error in the change and this will be treated as fatal.
func (p *WatchedIndex) AddListener(didChangeListener IndexChangeListener) {
	p.lock.Lock()
	p.listeners = append(p.listeners, didChangeListener)
	p.lock.Unlock()
}

// UnsafeSet sets the LogIndex to the given value and calls the registered listeners
// WITHOUT locking the underlying Locker.
// The caller MUST have locked the underlying Locker to ensure safe concurrent operation.
//
// After the value is changed, all registered listeners are called in order.
// Any listener can indicate an error and this will be treated as fatal.
//
// Since the underlying lock should be held during this call, the listener is guaranteed
// that another change will not occur until it has returned to this method.
// However, this also means that the listener will block all other callers
// to this WatchedIndex.
func (p *WatchedIndex) UnsafeSet(new LogIndex) error {
	var err error
	old := p.value
	p.value = new
	for _, f := range p.listeners {
		err = f(old, new)
		if err != nil {
			break
		}
	}
	return err
}