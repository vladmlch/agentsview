package failurecache

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/db"
)

var (
	poisonIdentity = Identity{MTimeNS: 1700000000000000000, Size: 128}
	changedID      = Identity{MTimeNS: 1700000001000000000, Size: 128}
)

// RecordFailure counts attempts against one identity, class, and data
// version, and resets the budget when any of the three changes.
func TestRecordFailureCountsAndResetsAttempts(t *testing.T) {
	var cache Cache
	key := "codex:/poison.jsonl"

	for attempt := 1; attempt <= MaxParseAttemptsPerIdentity; attempt++ {
		assert.Equal(t, attempt, cache.RecordFailure(
			key, poisonIdentity, ClassMalformedJSON, 7,
		))
	}

	// A stats change resets the budget: the source changed, so the poison
	// conclusion must be re-tested.
	assert.Equal(t, 1, cache.RecordFailure(
		key, changedID, ClassMalformedJSON, 7,
	))
	// A class change resets the budget: the failure mode changed, so a
	// parser fix may have landed without a source change.
	assert.Equal(t, 1, cache.RecordFailure(
		key, changedID, ClassShapeViolation, 7,
	))
	// A data-version change resets the budget: a parser upgrade
	// re-eligibilizes the source.
	assert.Equal(t, 1, cache.RecordFailure(
		key, changedID, ClassShapeViolation, 8,
	))
}

// Quarantined holds only when the recorded budget was spent against the
// identity being admitted at the archive's current data version.
func TestQuarantinedRequiresMatchingIdentityAndVersion(t *testing.T) {
	var cache Cache
	key := "codex:/poison.jsonl"
	for range MaxParseAttemptsPerIdentity {
		cache.RecordFailure(key, poisonIdentity, ClassMalformedJSON, 7)
	}

	class, attempts, ok := cache.Quarantined(key, poisonIdentity, 7)
	require.True(t, ok)
	assert.Equal(t, ClassMalformedJSON, class)
	assert.Equal(t, MaxParseAttemptsPerIdentity, attempts)

	// Same identity at a newer data version: the parser upgrade lifts the
	// quarantine.
	_, _, ok = cache.Quarantined(key, poisonIdentity, 8)
	assert.False(t, ok, "data-version change must lift the quarantine")

	// Unchanged version but changed identity: stats change lifts it.
	_, _, ok = cache.Quarantined(key, changedID, 7)
	assert.False(t, ok, "stats change must lift the quarantine")

	// An unknown key is never quarantined.
	_, _, ok = cache.Quarantined("codex:/other.jsonl", poisonIdentity, 7)
	assert.False(t, ok)
}

// Only deterministic parse poison counts against the budget; transient and
// missing-source failures retry on every pass.
func TestQuarantineIgnoresTransientAndMissingClasses(t *testing.T) {
	var cache Cache
	for _, class := range []Class{ClassTransient, ClassMissing, ""} {
		key := "codex:/" + string(class) + ".jsonl"
		for range MaxParseAttemptsPerIdentity + 1 {
			cache.RecordFailure(key, poisonIdentity, class, 7)
		}
		_, _, ok := cache.Quarantined(key, poisonIdentity, 7)
		assert.False(t, ok, "class %q must never quarantine", class)
	}
}

// A source below the cap is admitted: the skip must not bite before the
// budget is exhausted.
func TestQuarantinedHoldsOnlyAtCap(t *testing.T) {
	var cache Cache
	key := "codex:/poison.jsonl"
	cache.RecordFailure(key, poisonIdentity, ClassMalformedJSON, 7)
	cache.RecordFailure(key, poisonIdentity, ClassMalformedJSON, 7)
	_, _, ok := cache.Quarantined(key, poisonIdentity, 7)
	assert.False(t, ok, "below the cap the source stays admitted")

	cache.RecordFailure(key, poisonIdentity, ClassMalformedJSON, 7)
	_, _, ok = cache.Quarantined(key, poisonIdentity, 7)
	assert.True(t, ok, "at the cap the source is quarantined")
}

// A successful parse or an explicit refresh clears the whole ledger; a
// bypass that re-admits an unchanged source keeps it counting.
func TestClearWipesLedgerButClearEntryKeepsIt(t *testing.T) {
	var cache Cache
	key := "codex:/poison.jsonl"
	for range MaxParseAttemptsPerIdentity {
		cache.RecordFailure(key, poisonIdentity, ClassMalformedJSON, 7)
	}

	bypassed := &cache
	bypassed.ClearEntry(key)
	_, attempts, ok := bypassed.Ledger(key)
	require.True(t, ok)
	assert.Equal(t, MaxParseAttemptsPerIdentity, attempts,
		"a bypass must not refund the spent budget")

	cache.Clear(key)
	_, _, ok = cache.Ledger(key)
	assert.False(t, ok, "a success or explicit refresh forgets the ledger")
}

// A resync resets the whole ledger: an explicit rebuild must reconcile
// every source, including quarantined ones.
func TestResetWipesLedger(t *testing.T) {
	var cache Cache
	key := "codex:/poison.jsonl"
	for range MaxParseAttemptsPerIdentity {
		cache.RecordFailure(key, poisonIdentity, ClassMalformedJSON, 7)
	}
	cache.Reset()
	_, _, ok := cache.Ledger(key)
	assert.False(t, ok)
	_, _, ok = cache.Quarantined(key, poisonIdentity, 7)
	assert.False(t, ok)
}

// The ledger survives a restart: Flush encodes it into the durable rows'
// fingerprint extension and Load seeds it back.
func TestLedgerSurvivesRestart(t *testing.T) {
	database := openDB(t)
	var cache Cache
	cache.Record("codex:/kept.jsonl", poisonIdentity)
	for range MaxParseAttemptsPerIdentity {
		cache.RecordFailure(
			"codex:/poison.jsonl", poisonIdentity, ClassMalformedJSON, 7,
		)
	}
	require.NoError(t, cache.Flush(t.Context(), database))

	var restarted Cache
	require.NoError(t, restarted.Load(t.Context(), database))
	class, attempts, ok := restarted.Ledger("codex:/poison.jsonl")
	require.True(t, ok)
	assert.Equal(t, ClassMalformedJSON, class)
	assert.Equal(t, MaxParseAttemptsPerIdentity, attempts)
	_, _, ok = restarted.Ledger("codex:/kept.jsonl")
	assert.False(t, ok, "a row without a ledger stays ledger-free")
	// The restart's identity is intact: the skip still applies.
	assert.True(t, restarted.Check(
		"codex:/poison.jsonl", poisonIdentity,
	))
	// And the quarantined state is reachable again immediately.
	_, _, ok = restarted.Quarantined("codex:/poison.jsonl", poisonIdentity, 7)
	assert.True(t, ok)
}

// The durable fingerprint extension round-trips and leaves pre-ledger rows
// byte-identical.
func TestSourceFailureFingerprintEncoding(t *testing.T) {
	const hash = "sha256:abc123"
	assert.Equal(t, hash, db.EncodeSourceFailureFingerprint(hash, db.SourceFailureExtra{}))

	extra := db.SourceFailureExtra{Class: "malformed-json", Attempts: 3, DataVersion: 7}
	decodedHash, decoded := db.DecodeSourceFailureFingerprint(
		db.EncodeSourceFailureFingerprint(hash, extra),
	)
	assert.Equal(t, hash, decodedHash)
	assert.Equal(t, extra, decoded)

	// A plain hash with a separator-looking suffix that does not parse as
	// the extension shape stays verbatim.
	odd := hash + "\x1fonly-two-parts"
	decodedHash, decoded = db.DecodeSourceFailureFingerprint(odd)
	assert.Equal(t, odd, decodedHash)
	assert.Equal(t, db.SourceFailureExtra{}, decoded)
}

// ClassifyError names the deterministic parse classes and refuses the
// transient ones, including the unsupported-schema version lane that
// previously leaked past the marker classifier and re-parsed every pass.
func TestClassifyErrorNamesParseClasses(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		wantClass Class
		wantOK    bool
	}{
		{name: "malformed", err: errString("malformed source"), wantClass: ClassMalformedJSON, wantOK: true},
		{name: "syntax", err: errString("syntax error at line 3"), wantClass: ClassMalformedJSON, wantOK: true},
		{name: "unsupported schema", err: errString("omnigent: unsupported schema: new table"),
			wantClass: ClassUnsupportedVersion, wantOK: true},
		{name: "no such table", err: errString("no such table: messages"),
			wantClass: ClassShapeViolation, wantOK: true},
		{name: "invalid", err: errString("invalid session shape"), wantClass: ClassShapeViolation, wantOK: true},
		{name: "permission", err: errString("permission denied"), wantClass: ClassTransient},
		{name: "locked", err: errString("database is locked"), wantClass: ClassTransient},
		{name: "nil", err: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			class, ok := ClassifyError(test.err)
			assert.Equal(t, test.wantOK, ok)
			assert.Equal(t, test.wantClass, class)
		})
	}
}

type errString string

func (e errString) Error() string { return string(e) }
