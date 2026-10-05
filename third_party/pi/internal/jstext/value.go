package jstext

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math"
	"strconv"
)

// Parse decodes JSON text into the value JSON.parse returns, in
// encoding/json's shapes (nil, bool, string, []any, map[string]any) except
// that a number stays a json.Number: JSON.parse reads a number past float64's
// range as ±Infinity (see Number), where decoding into a float64 fails.
func Parse(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("jstext: data after the JSON value; pass exactly one value")
	}
	return v, nil
}

// Number is the JavaScript number a JSON number denotes: the nearest float64,
// ±Infinity past the range (JSON.parse("1e400") is Infinity) and zero below
// it.
func Number(n json.Number) float64 {
	f, err := strconv.ParseFloat(string(n), 64)
	var numErr *strconv.NumError
	if err != nil && !(errors.As(err, &numErr) && errors.Is(numErr.Err, strconv.ErrRange)) {
		return math.NaN()
	}
	return f
}

// Truthy is JavaScript's Boolean(value) for a value Parse returns: false for
// null, false, 0 and "", true for anything else, an empty array or object
// included.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case json.Number:
		f := Number(x)
		return f != 0 && !math.IsNaN(f)
	case string:
		return x != ""
	}
	return true
}

// Reparse is JSON.parse(JSON.stringify(v)) for a value Parse returns. The
// one change such a round trip makes to parsed JSON that String and Number
// can see is that a number JSON.parse read as ±Infinity comes back null,
// since JSON.stringify writes it so; Reparse makes that change at any depth,
// copying only the containers on the way to one.
func Reparse(v any) any {
	out, _ := reparse(v)
	return out
}

func reparse(v any) (any, bool) {
	switch x := v.(type) {
	case json.Number:
		if math.IsInf(Number(x), 0) {
			return nil, true
		}
	case []any:
		var copied []any
		for i, e := range x {
			r, changed := reparse(e)
			if changed && copied == nil {
				copied = append([]any(nil), x...)
			}
			if copied != nil {
				copied[i] = r
			}
		}
		if copied != nil {
			return copied, true
		}
	case map[string]any:
		var copied map[string]any
		for k, e := range x {
			if r, changed := reparse(e); changed {
				if copied == nil {
					copied = maps.Clone(x)
				}
				copied[k] = r
			}
		}
		if copied != nil {
			return copied, true
		}
	}
	return v, false
}
