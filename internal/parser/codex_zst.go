package parser

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// isCodexCompressedTranscript reports whether path is a zstd-compressed
// Codex rollout (rollout-*.jsonl.zst). Compressed rollouts are immutable
// archives: the compressed byte stream admits no byte-offset resumes, so
// they are full-parse-only everywhere the checkpoint lanes check.
func isCodexCompressedTranscript(path string) bool {
	return strings.HasSuffix(filepath.Base(path), ".jsonl.zst")
}

// codexTranscriptSupportsResume reports whether a Codex transcript path
// may earn parser checkpoints and incremental tail-appends. Compressed
// (.jsonl.zst) rollouts decompress through a temporary copy whose offsets
// describe decompressed bytes, and legacy whole-JSON (.json) containers
// carry no line-aligned record boundaries, so neither is delta-resumable:
// both must rebuild through a full parse on every change.
func codexTranscriptSupportsResume(path string) bool {
	return !isCodexCompressedTranscript(path) &&
		!strings.HasSuffix(filepath.Base(path), ".json")
}

// newCodexZstdDecoder builds the shared single-threaded zstd decoder for
// Codex rollouts, mirroring the DeepSeek Harness decoder's bounded
// concurrency and memory options.
func newCodexZstdDecoder(r io.Reader) (*zstd.Decoder, error) {
	return zstd.NewReader(
		r,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderLowmem(true),
	)
}

// openCodexTranscript opens a Codex rollout for line-oriented parsing and
// reports the stat identity the parse should run against. Plain
// .jsonl/.json rollouts return the file itself with a nil cleanup.
// Compressed .jsonl.zst rollouts decompress once into a temporary file:
// the Codex parse path relies on a real *os.File (ReadAt terminator
// probes, Seek for the whole-JSON recovery pass, and stat identity), which
// a streaming decoder cannot provide. cleanup removes that temporary file
// and must be called when parsing is done; it is nil for plain rollouts.
func openCodexTranscript(path string) (*os.File, os.FileInfo, func(), error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if !isCodexCompressedTranscript(path) {
		return f, info, nil, nil
	}
	defer f.Close()

	tmp, err := os.CreateTemp("", "agentsview-codex-zst-*.jsonl")
	if err != nil {
		return nil, nil, nil, fmt.Errorf(
			"decompress %s: creating temp file: %w", path, err,
		)
	}
	cleanup := func() { os.Remove(tmp.Name()) }
	decoder, err := newCodexZstdDecoder(io.LimitReader(f, info.Size()))
	if err != nil {
		tmp.Close()
		cleanup()
		return nil, nil, nil, fmt.Errorf(
			"decompress %s: open zstd stream: %w", path, err,
		)
	}
	if _, err := io.Copy(tmp, decoder); err != nil {
		decoder.Close()
		tmp.Close()
		cleanup()
		return nil, nil, nil, fmt.Errorf("decompress %s: %w", path, err)
	}
	decoder.Close()
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		cleanup()
		return nil, nil, nil, fmt.Errorf(
			"decompress %s: rewind temp file: %w", path, err,
		)
	}
	tmpInfo, err := tmp.Stat()
	if err != nil {
		tmp.Close()
		cleanup()
		return nil, nil, nil, fmt.Errorf(
			"decompress %s: stat temp file: %w", path, err,
		)
	}
	return tmp, tmpInfo, cleanup, nil
}

// rebindCodexZstSessionFile restores the source identity of a session
// parsed from a compressed rollout. The parse itself ran against the
// decompressed temporary copy, so the assembled session's file stat
// describes that temp file; the stored row must describe the on-disk
// compressed source the fingerprint and freshness gates measure.
func rebindCodexZstSessionFile(
	p *codexProvider, sess *ParsedSession, path string,
) {
	if sess == nil || !isCodexCompressedTranscript(path) {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	inode, device := sourceFileIdentityForPath(path, info)
	mtime := info.ModTime().UnixNano()
	if p.spec.agent == AgentCodex {
		mtime = p.sources.metadata.EffectiveMtime(path, mtime)
	}
	changeTime, _ := codexIndexChangeTime(path, info)
	sess.File.Size = info.Size()
	sess.File.Mtime = mtime
	sess.File.Inode = int64(inode)
	sess.File.Device = int64(device)
	sess.File.ChangeTime = changeTime
}
