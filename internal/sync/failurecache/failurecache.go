// Package failurecache remembers source files whose parse failed for a reason
// that will not change until the file does, so sync can skip them instead of
// failing the same way on every pass.
package failurecache

import (
	"context"
	"fmt"
	"maps"
	"sync"

	"go.kenn.io/agentsview/internal/db"
)

// Identity is what "unchanged" means for a source file.
type Identity = db.SourceFailure

// Cache is safe for concurrent use. The zero value is an empty cache.
//
// Memory is authoritative: entries describe source files, not archive rows, so
// they stay valid whichever archive database is live. Flush makes them survive
// a restart.
type Cache struct {
	// flushMu serializes whole flushes so an older snapshot cannot land after
	// a newer one.
	flushMu  sync.Mutex
	mu       sync.Mutex
	entries  map[string]Identity
	revision uint64
	// attempts is the bounded retry ledger keyed by cache key. It rides the
	// durable source_failures rows through the fingerprint column's
	// extension segment, so a restart keeps counting.
	attempts map[string]attemptRecord
	// flushedTo and flushedRevision identify the last durable copy, so an
	// unchanged cache costs no write on an idle pass.
	flushedTo       *db.DB
	flushedRevision uint64
}

// Load replaces the cache contents with the failures persisted in source.
// Durable bounded-retry ledgers seed the in-memory ledger, so a restart
// keeps counting a poison source's attempts.
func (c *Cache) Load(ctx context.Context, source *db.DB) error {
	loaded, extras, err := source.LoadSourceFailureDetails(ctx)
	if err != nil {
		return fmt.Errorf("loading failure cache: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = loaded
	c.attempts = nil
	for key, extra := range extras {
		id, ok := loaded[key]
		if !ok {
			continue
		}
		class := Class(extra.Class)
		if !class.QuarantineEligible() || extra.Attempts <= 0 {
			continue
		}
		if c.attempts == nil {
			c.attempts = make(map[string]attemptRecord)
		}
		c.attempts[key] = attemptRecord{
			identity:    id,
			class:       class,
			attempts:    extra.Attempts,
			dataVersion: extra.DataVersion,
		}
	}
	c.revision++
	c.flushedTo = source
	c.flushedRevision = c.revision
	return nil
}

// Lookup checks memory before the caller does filesystem or archive work.
func (c *Cache) Lookup(key string) (Identity, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.entries[key]
	return id, ok
}

// Merge preserves failures found by a resync contributor if its archive is discarded.
func (c *Cache) Merge(other *Cache) {
	other.mu.Lock()
	entries := maps.Clone(other.entries)
	attempts := maps.Clone(other.attempts)
	other.mu.Unlock()
	for key, id := range entries {
		c.Record(key, id)
	}
	if attempts == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, record := range attempts {
		if _, ok := c.attempts[key]; ok {
			continue
		}
		if c.attempts == nil {
			c.attempts = make(map[string]attemptRecord)
		}
		c.attempts[key] = record
		c.revision++
	}
}

// Check reports whether key is a known failure whose source still has
// identity id. An entry recorded for a different identity is stale and is
// removed, so the caller parses the changed source again.
func (c *Cache) Check(key string, id Identity) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	recorded, ok := c.entries[key]
	if !ok {
		return false
	}
	if recorded != id {
		delete(c.entries, key)
		c.revision++
		return false
	}
	return true
}

// Record remembers that the source behind key failed while it had identity id.
func (c *Cache) Record(key string, id Identity) {
	if key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if recorded, ok := c.entries[key]; ok && recorded == id {
		return
	}
	if c.entries == nil {
		c.entries = make(map[string]Identity)
	}
	c.entries[key] = id
	c.revision++
}

// Clear forgets key — its recorded identity and its bounded retry ledger —
// so its source is parsed again with a fresh budget. Successes and explicit
// refreshes clear this way; bypasses that re-admit an unchanged source use
// ClearEntry to keep the ledger counting.
func (c *Cache) Clear(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, hasEntry := c.entries[key]
	_, hasLedger := c.attempts[key]
	if !hasEntry && !hasLedger {
		return
	}
	delete(c.entries, key)
	delete(c.attempts, key)
	c.revision++
}

// Reset forgets every entry and the whole retry ledger. The engine calls it
// wherever it discards all other freshness shortcuts, so the following pass
// parses every source again — including quarantined ones: a resync is how a
// parser upgrade reconciles sources the previous parser gave up on.
func (c *Cache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) == 0 && len(c.attempts) == 0 {
		return
	}
	c.entries = nil
	c.attempts = nil
	c.revision++
}

// Flush persists the cache into target. It writes nothing when target already
// holds the current contents.
func (c *Cache) Flush(ctx context.Context, target *db.DB) error {
	c.flushMu.Lock()
	defer c.flushMu.Unlock()
	c.mu.Lock()
	if target == c.flushedTo && c.revision == c.flushedRevision {
		c.mu.Unlock()
		return nil
	}
	snapshot := maps.Clone(c.entries)
	revision := c.revision
	ledger := maps.Clone(c.attempts)
	c.mu.Unlock()

	// The bounded retry ledger rides each row's fingerprint column through an
	// extension segment, so a restart keeps counting a poison source's
	// attempts without a schema change.
	for key, id := range snapshot {
		record, ok := ledger[key]
		if !ok {
			continue
		}
		id.Fingerprint = db.EncodeSourceFailureFingerprint(id.Fingerprint,
			db.SourceFailureExtra{
				Class:       string(record.class),
				Attempts:    record.attempts,
				DataVersion: record.dataVersion,
			})
		snapshot[key] = id
	}

	if err := target.ReplaceSourceFailures(ctx, snapshot); err != nil {
		return fmt.Errorf("persisting failure cache: %w", err)
	}
	c.mu.Lock()
	c.flushedTo = target
	c.flushedRevision = revision
	c.mu.Unlock()
	return nil
}
