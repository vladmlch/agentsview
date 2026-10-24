package failurecache

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"strings"
)

// Class names the reason a source's parse failed. Deterministic classes are
// parse poison: the same source bytes fail the same way until the source
// changes, so sync quarantines them behind a bounded retry budget instead of
// re-parsing them on every pass. Transient classes are not quarantined: the
// next pass may legitimately succeed.
type Class string

const (
	// ClassMalformedJSON: the source bytes are not decodable JSON — a
	// syntactic decode error, a truncated or malformed document, or an
	// unexpected token.
	ClassMalformedJSON Class = "malformed-json"
	// ClassShapeViolation: the source decodes but its shape is wrong for the
	// parser — a semantic decode error, an invalid field or row shape, or a
	// container store whose schema rows are missing.
	ClassShapeViolation Class = "shape-violation"
	// ClassUnsupportedVersion: the parser recognizes the source as a format
	// or schema version it cannot decode yet.
	ClassUnsupportedVersion Class = "unsupported-version"
	// ClassParse: a deterministic parse failure whose cause the classifier
	// could not name — for example the outcome validator rejecting a
	// provider's parse result.
	ClassParse Class = "parse-failed"
	// ClassTransient: an error that a later pass can recover from without
	// the source changing — IO busy, timeouts, permission, missing file.
	ClassTransient Class = "transient-io"
	// ClassMissing: the source did not exist at failure time. Recorded so
	// the failure-cache lane can skip until the file returns, but never
	// quarantined.
	ClassMissing Class = "source-missing"
)

// Recordable reports whether a failure of this class belongs in the failure
// cache at all. Transient failures change nothing in the recorded identity,
// so recording them would skip a source that the next pass could parse.
func (c Class) Recordable() bool {
	return c != "" && c != ClassTransient
}

// QuarantineEligible reports whether failures of this class count against
// the bounded retry budget. Only deterministic parse poison does; a missing
// source or a transient IO error must keep retrying every pass.
func (c Class) QuarantineEligible() bool {
	switch c {
	case ClassMalformedJSON, ClassShapeViolation, ClassUnsupportedVersion, ClassParse:
		return true
	default:
		return false
	}
}

// ClassifyError inspects a parse error and names its failure class. ok
// reports whether the error is recordable — deterministic enough that a
// later pass over the unchanged source would fail the same way. The paths
// are the source paths the error may name: parser wrappers embed them, and
// a directory name must not classify an otherwise transient error as
// malformed (or vice versa).
//
// Structured causes are classified before the message is inspected, so a
// wrapped *jsontext.SyntacticError stays malformed-json even when the
// wrapper text mentions a path. The message markers then separate
// transient causes (record nothing — the next pass may succeed) from
// deterministic poison (record with the named class).
func ClassifyError(err error, paths ...string) (Class, bool) {
	if err == nil {
		return "", false
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) ||
		errors.Is(err, os.ErrPermission) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ClassTransient, false
	}
	// Classify structured causes before inspecting wrappers, which can include
	// companion paths as well as the primary source path.
	if _, ok := errors.AsType[*os.PathError](err); ok {
		return ClassTransient, false
	}
	if _, ok := errors.AsType[*jsontext.SyntacticError](err); ok {
		return ClassMalformedJSON, true
	}
	if _, ok := errors.AsType[*json.SemanticError](err); ok {
		return ClassShapeViolation, true
	}
	message := err.Error()
	// Parser wrappers include source paths. Directory names must not classify
	// an otherwise transient error as malformed (or vice versa).
	for _, path := range paths {
		if path != "" {
			message = strings.ReplaceAll(message, path, "")
		}
	}
	lower := strings.ToLower(message)
	for _, marker := range []string{
		"permission", "access is denied", "temporary", "temporarily",
		"timed out", "timeout", "database is locked", "database is busy",
		"resource temporarily unavailable", "try again", "read ", "reading ",
		"scan ", "scanning ", "open ", "stat ", "i/o", "input/output",
		"unexpected eof", "truncated", "incomplete",
	} {
		if strings.Contains(lower, marker) {
			return ClassTransient, false
		}
	}
	for _, marker := range []string{
		"unsupported schema", "unsupported version", "unsupported format",
		"unsupported rollout",
	} {
		if strings.Contains(lower, marker) {
			return ClassUnsupportedVersion, true
		}
	}
	for _, marker := range []string{"no such column", "no such table"} {
		if strings.Contains(lower, marker) {
			return ClassShapeViolation, true
		}
	}
	for _, marker := range []string{
		"malformed", "syntax", "unexpected token", "invalid character",
		"invalid json",
	} {
		if strings.Contains(lower, marker) {
			return ClassMalformedJSON, true
		}
	}
	if strings.Contains(lower, "invalid") {
		return ClassShapeViolation, true
	}
	return "", false
}
