package parser

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// jsonContainerJSONLReader adapts a whole-JSON array container to the
// JSONL byte stream the shared line readers expect: each top-level array
// element is re-emitted verbatim as one line. Detection peeks the first
// non-decoded byte; any input that does not open with '[' passes through
// unchanged, so JSONL content living in a .json file keeps parsing line
// by line. The converter only pulls from the wrapped reader as callers
// consume it, so a hash tee below the converter still sees the original
// container bytes in order.
type jsonContainerJSONLReader struct {
	br   *bufio.Reader
	dec  *json.Decoder
	buf  []byte // pending re-emitted bytes (record + newline)
	done bool
	err  error
}

// newJSONContainerJSONLReader returns a reader that flattens a JSON array
// container into JSONL lines when r begins with '[', and r itself
// (buffered) otherwise.
func newJSONContainerJSONLReader(r io.Reader) io.Reader {
	br := bufio.NewReader(r)
	head, err := br.Peek(1)
	if err != nil || head[0] != '[' {
		return br
	}
	c := &jsonContainerJSONLReader{br: br, dec: json.NewDecoder(br)}
	// Position the stream inside the array: consuming the opening '['
	// token makes the subsequent More/Decode pairs yield the array's
	// elements one by one instead of the whole array as a single value.
	if tok, tokErr := c.dec.Token(); tokErr != nil || tok != json.Delim('[') {
		c.err = fmt.Errorf("json container: expected array start: %v", tokErr)
	}
	return c
}

func (c *jsonContainerJSONLReader) Read(p []byte) (int, error) {
	for len(c.buf) == 0 {
		if c.err != nil {
			return 0, c.err
		}
		if c.done {
			return 0, io.EOF
		}
		if !c.dec.More() {
			// Drain the array terminator and any trailing bytes so a
			// hash tee below the converter observes the whole source.
			if _, err := c.dec.Token(); err == nil {
				_, _ = io.Copy(io.Discard, c.br)
			}
			c.done = true
			continue
		}
		var raw json.RawMessage
		if err := c.dec.Decode(&raw); err != nil {
			c.err = err
			continue
		}
		c.buf = append(c.buf[:0], raw...)
		c.buf = append(c.buf, '\n')
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}
