package delta

import "fmt"

// ─── Codec ───────────────────────────────────────────────────────────────────
//
// Path interning and arity omission live between the tracker and a boundary;
// Op and the appliers know nothing about them.
//
// ONE PAIR PER INDEPENDENT STATE STREAM. Every decoder must observe exactly
// the batches encoded by its matching encoder, beginning with that state's
// base. Sharing a transport connection does not make separately hydrated
// states one stream.

// Encoder compresses the paths of decoded batches into wire batches. It
// interns a path on its SECOND use — a definition costs more than the path it
// replaces, so interning on first use loses on the many paths written exactly
// once — and drops the path from an op that repeats the previous op's.
//
// The encoder is stateful: ids span batches until a Replace resets them. Use
// one per ordered stream. The zero value is ready to use.
type Encoder struct {
	seen map[string]bool   // paths used once; the next use defines an id
	ids  map[string]PathID // paths used twice or more, by their id
	next PathID
}

// Encode returns the wire form of one batch. It does not validate: a batch a
// tracker prepared is already well-formed, and a decoder checks what it
// receives.
// A nil op is a programming error and panics.
func (e *Encoder) Encode(ops []Op) []WireOp {
	// Arity omission is scoped to a batch. Letting it span batches would make
	// a batch's first op depend on the previous batch's last one, so a reader
	// that skips or reorders a batch decodes into the wrong path. Ids are the
	// only cross-batch state, and the dictionary makes those explicit.
	//
	// previous is the last path's dictionary key in THIS batch. Path.String()
	// is never empty — the root is "[]" — so the empty string is "none".
	previous := ""
	out := make([]WireOp, 0, len(ops))
	for _, op := range ops {
		if r, ok := op.(Replace); ok {
			out = append(out, r)
			// A base batch is a RECOVERY POINT: a reader replays from the last
			// one with a fresh decoder. So everything after it must be
			// self-contained. Keeping ids across a replacement emits references
			// to definitions the reader never saw — recovery fails with an
			// unresolvable path id.
			clear(e.seen)
			clear(e.ids)
			e.next = 0
			previous = ""
			continue
		}
		path := opPath(op)
		// The JSON form: a joined string would collide on keys containing the
		// separator, and JSON cannot.
		key := path.String()

		// Same path as the previous op: drop the ref entirely.
		if key == previous {
			out = append(out, toWire(op, nil))
			continue
		}

		var ref PathRef = path
		if id, ok := e.ids[key]; ok {
			ref = id
		} else if e.seen[key] {
			id := e.next
			e.next++
			if e.ids == nil {
				e.ids = map[string]PathID{}
			}
			e.ids[key] = id
			out = append(out, Define{ID: id, Path: path}) // second use: define, then reference
			ref = id
		} else {
			if e.seen == nil {
				e.seen = map[string]bool{}
			}
			e.seen[key] = true // first use: inline
		}
		out = append(out, toWire(op, ref))
		previous = key
	}
	return out
}

// opPath is the path a decoded op addresses. Replace addresses the root and
// has no path of its own; the encoder handles it before this is reached.
func opPath(op Op) Path {
	switch op := op.(type) {
	case Replace:
		return nil
	case Set:
		return op.Path
	case Delete:
		return op.Path
	case Append:
		return op.Path
	case Truncate:
		return op.Path
	case Splice:
		return op.Path
	case Permute:
		return op.Path
	}
	// Op is sealed; only a nil element reaches here, and a batch with a hole
	// is a bug in the producer, not bad input.
	panic(fmt.Errorf("delta: cannot encode %T (a batch is a []Op of Replace, Set, Delete, Append, Truncate, Splice or Permute with no nil element)", op))
}

// toWire is op with its path replaced by ref: an inline path, an id, or nil
// for the short form that reuses the previous op's path.
func toWire(op Op, ref PathRef) WireOp {
	switch op := op.(type) {
	case Set:
		return WireSet{Ref: ref, Value: op.Value}
	case Delete:
		return WireDelete{Ref: ref}
	case Append:
		return WireAppend{Ref: ref, Text: op.Text}
	case Truncate:
		return WireTruncate{Ref: ref, Count: op.Count}
	case Splice:
		return WireSplice{Ref: ref, Index: op.Index, Remove: op.Remove, Items: op.Items}
	case Permute:
		return WirePermute{Ref: ref, Permutation: op.Permutation}
	}
	panic(fmt.Errorf("delta: cannot encode %T", op)) // unreachable: opPath ran first
}

// Decoder restores the complete paths of wire batches and returns the decoded
// ops an applier takes. It remembers the ids its encoder defined until a
// Replace clears them, so replay can begin at any base batch with a fresh
// decoder. Use one per ordered stream. The zero value is ready to use.
type Decoder struct {
	paths map[PathID]Path
}

// Decode validates every wire op — ParseWireOp's constraints, re-checked on
// the typed value — resolves ids and short forms, and returns the batch with
// paths inline. An error terminates the stream: discard the decoder and its
// replica and recover from a later base batch. The error wraps the op's own
// (*PathError for an unresolvable id or a short form with nothing before it,
// *UnsafePathError for a reserved segment, ErrInvalidOp for a shape fault) and
// names the op's index in the batch.
func (d *Decoder) Decode(wire []WireOp) ([]Op, error) {
	// Scoped to the batch, as in Encode. A root path is empty, so a separate
	// flag says whether there is a previous path at all.
	var previous Path
	hasPrevious := false
	out := make([]Op, 0, len(wire))
	for i, op := range wire {
		if op == nil {
			return nil, fmt.Errorf("delta: wire[%d]: %w: op is nil (a wire batch is a []WireOp of the values ParseWireOp returns)", i, ErrInvalidOp)
		}
		if err := op.Validate(); err != nil {
			return nil, fmt.Errorf("delta: wire[%d]: %w", i, err)
		}
		switch op := op.(type) {
		case Define:
			if d.paths == nil {
				d.paths = map[PathID]Path{}
			}
			d.paths[op.ID] = op.Path
			continue
		case Replace:
			out = append(out, op)
			clear(d.paths)
			hasPrevious = false
			continue
		}

		// Arity told the parser whether a ref is present: the short forms
		// omit it, and carry a nil Ref here.
		var path Path
		switch ref := wireRef(op).(type) {
		case nil:
			if !hasPrevious {
				return nil, fmt.Errorf("delta: wire[%d]: %w (the short form reuses the previous op's path, and nothing before it in this batch has one; path omission never spans batches)", i, &PathError{Ref: Path{}})
			}
			path = previous
		case PathID:
			resolved, ok := d.paths[ref]
			if !ok {
				return nil, fmt.Errorf("delta: wire[%d]: %w (no \"#\" op has defined path id %d since the stream's last base batch; decode every batch of one stream with one decoder, starting at a base batch)", i, &PathError{Ref: ref}, ref)
			}
			path = resolved
			previous, hasPrevious = path, true
		case Path:
			path = ref
			previous, hasPrevious = path, true
		}

		decoded := fromWire(op, path)
		if !rootable(decoded) && len(path) == 0 {
			return nil, fmt.Errorf("delta: wire[%d]: %w (only \"r\" replaces the root, and only \"p\" and \"m\" may splice or permute a root array)", i, &PathError{Ref: path})
		}
		out = append(out, decoded)
	}
	return out, nil
}

// wireRef is the path ref a wire op carries: a Path, a PathID, or nil for the
// short form. Replace and Define are handled before this is reached.
func wireRef(op WireOp) PathRef {
	switch op := op.(type) {
	case WireSet:
		return op.Ref
	case WireDelete:
		return op.Ref
	case WireAppend:
		return op.Ref
	case WireTruncate:
		return op.Ref
	case WireSplice:
		return op.Ref
	case WirePermute:
		return op.Ref
	}
	panic(fmt.Errorf("delta: cannot decode %T", op)) // unreachable: WireOp is sealed
}

// fromWire is op with its ref replaced by the resolved path.
func fromWire(op WireOp, path Path) Op {
	switch op := op.(type) {
	case WireSet:
		return Set{Path: path, Value: op.Value}
	case WireDelete:
		return Delete{Path: path}
	case WireAppend:
		return Append{Path: path, Text: op.Text}
	case WireTruncate:
		return Truncate{Path: path, Count: op.Count}
	case WireSplice:
		return Splice{Path: path, Index: op.Index, Remove: op.Remove, Items: op.Items}
	case WirePermute:
		return Permute{Path: path, Permutation: op.Permutation}
	}
	panic(fmt.Errorf("delta: cannot decode %T", op)) // unreachable: wireRef ran first
}

// rootable reports whether op may address the root with a path: only a
// Splice or a Permute can, because a tracked value can itself be an array.
func rootable(op Op) bool {
	switch op.(type) {
	case Splice, Permute:
		return true
	}
	return false
}
