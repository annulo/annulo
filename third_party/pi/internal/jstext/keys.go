package jstext

import (
	"math"
	"strconv"
)

// ArrayIndexKey reports whether name is an array index in the ECMAScript
// sense — the canonical decimal form of an integer in [0, 2^32-2], so that
// `ToString(ToUint32(name)) === name` and the value is not 2^32-1 — and
// returns that value. An object lists these keys first, ascending, and every
// other key after them in creation order (OrdinaryOwnPropertyKeys), which is
// the order JSON.stringify, Object.keys and Object.entries follow. "0" and
// "12" qualify while "01", "-1", "1.0" and "4294967295" are ordinary keys.
func ArrayIndexKey(name string) (uint32, bool) {
	if name == "" || len(name) > 10 {
		return 0, false
	}
	if name[0] == '0' && len(name) > 1 {
		return 0, false // canonical form has no leading zeros
	}
	for i := 0; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseUint(name, 10, 64)
	if err != nil || value >= math.MaxUint32 {
		return 0, false
	}
	return uint32(value), true
}
