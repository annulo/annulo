package delta

import "iter"

// orderedMap is a JavaScript Map over a Go map: it enumerates in insertion
// order, setting an existing key keeps its place, and a deleted key that is set
// again goes to the end. The overlay emits ops in the order its writes,
// deletions and overrides were first made, as upstream's Maps and Sets do, so
// that order has to be kept. The zero value is an empty map.
type orderedMap[K comparable, V any] struct {
	entries []orderedEntry[K, V]
	at      map[K]int // the index in entries of each live key
}

type orderedEntry[K comparable, V any] struct {
	key   K
	value V
	live  bool
}

func (m *orderedMap[K, V]) len() int { return len(m.at) }

func (m *orderedMap[K, V]) get(k K) (V, bool) {
	i, ok := m.at[k]
	if !ok {
		var zero V
		return zero, false
	}
	return m.entries[i].value, true
}

func (m *orderedMap[K, V]) has(k K) bool {
	_, ok := m.at[k]
	return ok
}

func (m *orderedMap[K, V]) set(k K, v V) {
	if i, ok := m.at[k]; ok {
		m.entries[i].value = v
		return
	}
	if m.at == nil {
		m.at = map[K]int{}
	}
	m.at[k] = len(m.entries)
	m.entries = append(m.entries, orderedEntry[K, V]{key: k, value: v, live: true})
}

func (m *orderedMap[K, V]) delete(k K) {
	i, ok := m.at[k]
	if !ok {
		return
	}
	delete(m.at, k)
	m.entries[i] = orderedEntry[K, V]{}
	if len(m.at) == 0 {
		m.entries = m.entries[:0]
	} else if len(m.entries) > 32 && len(m.at) < len(m.entries)/2 {
		m.compact()
	}
}

// compact drops the dead entries, keeping the live ones in order.
func (m *orderedMap[K, V]) compact() {
	live := m.entries[:0]
	for _, e := range m.entries {
		if e.live {
			m.at[e.key] = len(live)
			live = append(live, e)
		}
	}
	clear(m.entries[len(live):])
	m.entries = live
}

// all yields the live entries in insertion order.
func (m *orderedMap[K, V]) all() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for _, e := range m.entries {
			if e.live && !yield(e.key, e.value) {
				return
			}
		}
	}
}

// keys yields the live keys in insertion order.
func (m *orderedMap[K, V]) keys() iter.Seq[K] {
	return func(yield func(K) bool) {
		for _, e := range m.entries {
			if e.live && !yield(e.key) {
				return
			}
		}
	}
}
