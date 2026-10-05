package providers

import (
	"fmt"
	"io"
)

// maxEmptyBodyReads is how many reads in a row may return no data and no
// error before a response body is taken for broken, bufio.Scanner's own
// limit.
const maxEmptyBodyReads = 100

// progressReader is a response body as an SSE reader reads it: one whose
// reads keep returning neither data nor an error — which io.Reader's
// contract discourages and no network body does — fails once
// maxEmptyBodyReads of them come in a row, where pi's reader would wait on
// it forever (a port guard, docs/UPSTREAM.md D83). Empty reads between ones
// that return data do not add up.
type progressReader struct {
	r        io.Reader
	provider string
	empties  int
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 || err != nil || len(b) == 0 {
		p.empties = 0
		return n, err
	}
	if p.empties++; p.empties >= maxEmptyBodyReads {
		return 0, bodyNoProgressError(p.provider)
	}
	return 0, nil
}

// bodyNoProgressError is progressReader's error for provider's body: it
// says the guard is the port's and what to report, and wraps
// io.ErrNoProgress.
func bodyNoProgressError(provider string) error {
	return fmt.Errorf("the %s response body returned no data and no error %d reads in a row; this is a port guard against a broken body reader, report it with the provider: %w", provider, maxEmptyBodyReads, io.ErrNoProgress)
}
