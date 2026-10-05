package cbor

import (
	"reflect"
	"sync"
)

// typeCache memoises the reflected field layout of a struct type. Encoding runs
// per protocol frame, so resolving tags once per type rather than per frame is
// worth the small amount of machinery.
//
// A rejected layout is cached too: a struct whose field set cannot be encoded is
// a program bug, and it must be reported identically on every attempt.
type typeCache struct{ m sync.Map }

func newTypeCache() *typeCache { return &typeCache{} }

func (c *typeCache) load(t reflect.Type) (structLayout, bool) {
	v, ok := c.m.Load(t)
	if !ok {
		return structLayout{}, false
	}
	return v.(structLayout), true
}

func (c *typeCache) store(t reflect.Type, layout structLayout) {
	c.m.Store(t, layout)
}
