package failurecache

// MaxParseAttemptsPerIdentity caps how many parse attempts one source
// identity may spend on one deterministic failure class before the engine
// skips its admission without re-parsing it. The budget resets when the
// recorded identity changes (the source changed), when the failure class
// changes (the failure mode changed — a parser fix may land without a
// source change), or when the archive data version changes (a parser
// upgrade re-eligibilizes quarantined sources). A resync resets the whole
// ledger: an explicit rebuild must reconcile every source.
const MaxParseAttemptsPerIdentity = 3

// attemptRecord is the bounded retry ledger entry for one cache key: the
// identity the attempts were spent against, the failure class they
// produced, the archive data version at record time, and the attempt count.
type attemptRecord struct {
	identity    Identity
	class       Class
	attempts    int
	dataVersion int
}

// RecordFailure records a parse failure of the given class against identity
// id at archive data version dataVersion, and returns the attempt count the
// identity has now spent. An attempt against the same identity, class, and
// data version increments the count; any change to those three resets the
// budget to one attempt. Failures that are not quarantine-eligible still
// update the recorded identity but never count.
func (c *Cache) RecordFailure(key string, id Identity, class Class, dataVersion int) int {
	c.Record(key, id)
	if !class.QuarantineEligible() {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	prior, ok := c.attempts[key]
	count := 1
	if ok && prior.class == class && prior.identity == id &&
		prior.dataVersion == dataVersion {
		count = prior.attempts + 1
	}
	if c.attempts == nil {
		c.attempts = make(map[string]attemptRecord)
	}
	c.attempts[key] = attemptRecord{
		identity:    id,
		class:       class,
		attempts:    count,
		dataVersion: dataVersion,
	}
	c.revision++
	return count
}

// Quarantined reports whether key has exhausted its bounded retry budget
// against identity id at archive data version dataVersion. A ledger entry
// recorded for a different identity, a different failure class history, or
// a different data version does not quarantine: the source changed, the
// failure mode changed, or a parser upgrade re-eligibilized it.
func (c *Cache) Quarantined(
	key string, id Identity, dataVersion int,
) (class Class, attempts int, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	record, exists := c.attempts[key]
	if !exists || record.identity != id || record.dataVersion != dataVersion ||
		record.attempts < MaxParseAttemptsPerIdentity {
		return "", 0, false
	}
	if !record.class.QuarantineEligible() {
		return "", 0, false
	}
	return record.class, record.attempts, true
}

// Ledger reports the recorded attempt state for key, for surfaces that
// display the bounded retry budget. ok is false when no quarantine-eligible
// attempt was ever counted for key.
func (c *Cache) Ledger(key string) (class Class, attempts int, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	record, exists := c.attempts[key]
	if !exists || !record.class.QuarantineEligible() {
		return "", 0, false
	}
	return record.class, record.attempts, true
}

// ClearEntry forgets the recorded identity for key but keeps its bounded
// retry ledger. Parse-admission bypasses that re-admit a source without the
// source changing — a stale data-version row or a project reparse — use it,
// so a poison source cannot loop unboundedly through the bypass every pass.
func (c *Cache) ClearEntry(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; !ok {
		return
	}
	delete(c.entries, key)
	c.revision++
}

// forgetLedger drops key's ledger entry; the caller holds c.mu.
func (c *Cache) forgetLedger(key string) {
	if _, ok := c.attempts[key]; !ok {
		return
	}
	delete(c.attempts, key)
	c.revision++
}
