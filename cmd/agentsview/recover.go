// ABOUTME: `agentsview recover <archive> <dest.db>` — salvage readable
// ABOUTME: content from a damaged transfer tar or SQLite snapshot into a
// ABOUTME: fresh archive. Read-only on the source, additive-only on dest.
package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-sqlite3"
	"github.com/spf13/cobra"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/remotesync"
)

const (
	recoverTmpSuffix       = backupTmpSuffix
	recoverPageSizeDefault = 512
	// recoverReservedPrefix names the auxiliary tables recover adds to
	// the fresh destination. They are additive bookkeeping, never part
	// of the archive contract, and can be dropped once the recovered
	// content has been carried onward.
	recoverReservedPrefix = "recover_"
)

// sqliteHeaderMagic is the first 16 bytes of every SQLite database file.
var sqliteHeaderMagic = []byte("SQLite format 3\x00")

// tarHeaderMagicOffset is where the ustar magic sits inside a tar header
// block. av's transfer archives are PAX tars (internal/remotesync), whose
// headers carry "ustar" at this offset.
const tarHeaderMagicOffset = 257

// recoverSkip is one entry (or file region) that could not be recovered,
// with the byte offset in the source where the problem was met.
type recoverSkip struct {
	Name   string `json:"name"`
	Offset int64  `json:"offset"`
	Reason string `json:"reason"`
}

// recoverRowSkip is one source row that was not imported. Identity
// violations (a primary key or uniqueness contract already satisfied by an
// earlier recovered row) keep the first occurrence and record the rest;
// rows that violate their table's other constraints are recorded here too,
// never rewritten into shape.
type recoverRowSkip struct {
	Table       string `json:"table"`
	SourceRowID int64  `json:"source_rowid"`
	Key         string `json:"key"`
	Reason      string `json:"reason"`
}

// recoverBreakpoint marks where keyset pagination stopped inside one
// table: rows after last_rowid could not be proven readable, so they were
// left unrecovered rather than guessed.
type recoverBreakpoint struct {
	Table     string `json:"table"`
	LastRowID int64  `json:"last_rowid"`
	RowsSoFar int64  `json:"rows_so_far"`
	Reason    string `json:"reason"`
}

// recoverTableSkip records a table whose rows were not importable at all.
type recoverTableSkip struct {
	Table  string `json:"table"`
	Reason string `json:"reason"`
}

// recoverReport is the per-run accounting. Everything the command did is
// additive on the destination; the source is only ever read.
type recoverReport struct {
	Source           string              `json:"source"`
	Destination      string              `json:"destination"`
	Lane             string              `json:"lane"`
	EntriesTotal     int                 `json:"entries_total"`
	EntriesRecovered int                 `json:"entries_recovered"`
	EntriesSkipped   []recoverSkip       `json:"entries_skipped"`
	RowsPerTable     map[string]int64    `json:"rows_per_table"`
	RowSkips         []recoverRowSkip    `json:"identity_skips"`
	TableSkips       []recoverTableSkip  `json:"tables_skipped,omitempty"`
	Breakpoints      []recoverBreakpoint `json:"pagination_breakpoints,omitempty"`
}

func (r *recoverReport) recordSkip(name string, offset int64, reason string) {
	r.EntriesSkipped = append(r.EntriesSkipped, recoverSkip{
		Name:   recoverPrintable(name),
		Offset: offset,
		Reason: recoverPrintable(reason),
	})
}

// recoverPrintable coerces report text to valid UTF-8: damaged sources can
// echo arbitrary bytes into names, keys, and SQLite diagnostics, and the
// report must stay valid JSON.
func recoverPrintable(s string) string {
	return strings.ToValidUTF8(s, string(utf8.RuneError))
}

// recoverCounters accumulates the row-import accounting of one SQLite
// source (a standalone snapshot or one tar entry).
type recoverCounters struct {
	rowsPerTable map[string]int64
	rowSkips     []recoverRowSkip
	tableSkips   []recoverTableSkip
	breakpoints  []recoverBreakpoint
}

func newRecoverCounters() *recoverCounters {
	return &recoverCounters{rowsPerTable: make(map[string]int64)}
}

func newRecoverCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recover <archive> <destination>",
		Short: "Salvage readable content from a damaged archive into a new database",
		Long: "Salvage readable content from a damaged av archive into a fresh\n" +
			"database at <destination>, which must not exist. The source archive\n" +
			"is only ever opened read-only and is never modified.\n\n" +
			"Two source formats are recognized by content: a remotesync transfer\n" +
			"tar (PAX, as written by remote sync) and a standalone SQLite\n" +
			"snapshot (as written by 'agentsview backup').\n\n" +
			"Tar lane: entries are recovered leniently — every entry that parses\n" +
			"fully (valid header, complete content, correct framing) is kept, and\n" +
			"the first structural damage (bad header, truncated content) ends the\n" +
			"recovery with the damaged region recorded by name, offset, and\n" +
			"reason. Unlike 'remote sync' extraction, which is fail-closed and\n" +
			"requires the 1024-byte tar end marker, recovery accepts a missing\n" +
			"end marker and never demands anything beyond what is readable. It\n" +
			"also never scans past damage for misaligned data: a guessed header\n" +
			"would fabricate an entry. Damaged entries are skipped and recorded,\n" +
			"never reconstructed.\n\n" +
			"SQLite entries inside the tar are verified with PRAGMA quick_check;\n" +
			"healthy ones have their rows imported, corrupt ones are recorded and\n" +
			"skipped. Row import is additive-only into a fresh archive built by\n" +
			"the engine's schema initializer: duplicate primary keys keep the\n" +
			"first occurrence and record the rest (never overwrite; the fresh\n" +
			"destination's own seeded identity rows are first occurrences), rows\n" +
			"that violate their constraints are quarantined in the report, and\n" +
			"every table is read with rowid keyset pagination so a corrupt page\n" +
			"cuts one table at its breakpoint and leaves the other tables\n" +
			"intact. Recovered entry bytes are additionally stored verbatim in\n" +
			"a recover_artifacts table so nothing readable is lost; virtual\n" +
			"(FTS) tables and their shadow tables are not recovered — they are\n" +
			"derived state a writable open rebuilds.\n\n" +
			"SQLite lane: a standalone snapshot that passes PRAGMA quick_check is\n" +
			"copied straight through the online-backup API (no reconstruction,\n" +
			"best fidelity). A damaged snapshot is recovered table by table with\n" +
			"the same keyset pagination: a read error mid-table records the\n" +
			"breakpoint and continues with the next table.\n\n" +
			"The destination is written to <destination>.tmp and renamed into\n" +
			"place only after the run, so it never holds a partial recovery.",
		GroupID:      groupData,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pageSize, err := cmd.Flags().GetInt("page-size")
			if err != nil {
				return err
			}
			if pageSize < 1 {
				return errors.New("--page-size must be a positive row count")
			}
			return runRecover(
				cmd.Context(), args[0], args[1], pageSize,
				outputFormat(cmd) == "json", cmd.OutOrStdout(),
			)
		},
	}
	cmd.Flags().Int("page-size", recoverPageSizeDefault,
		"Rows per keyset-pagination page when walking damaged tables")
	registerFormatFlags(cmd.Flags())
	return cmd
}

// runRecover performs the whole recovery. The source is opened read-only
// and the destination is installed atomically at the end.
func runRecover(
	ctx context.Context, source, dest string, pageSize int,
	jsonOutput bool, out io.Writer,
) (err error) {
	source = filepath.Clean(source)
	dest = filepath.Clean(dest)
	if source == dest {
		return errors.New(
			"recovery destination must differ from the source archive")
	}
	if _, err := checkRegularFile(source, "source archive"); err != nil {
		return err
	}
	if err := refuseExistingRecoverTarget(dest); err != nil {
		return err
	}

	tmp := dest + recoverTmpSuffix
	_ = os.Remove(tmp)
	report := &recoverReport{Source: source, Destination: dest}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(tmp)
		}
	}()

	lane, err := detectRecoverLane(source)
	if err != nil {
		return err
	}
	switch lane {
	case recoverLaneTar:
		err = recoverFromTar(ctx, source, tmp, pageSize, report)
	case recoverLaneSQLite:
		err = recoverFromSQLiteFile(ctx, source, tmp, pageSize, report)
	default:
		err = fmt.Errorf(
			"%s is neither a remotesync transfer tar nor a SQLite snapshot",
			source)
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("installing recovery destination %s: %w", dest, err)
	}
	complete = true

	if jsonOutput {
		return writeRecoverReportJSON(out, report)
	}
	return writeRecoverReportHuman(out, report)
}

type recoverLaneKind string

const (
	recoverLaneTar    recoverLaneKind = "tar"
	recoverLaneSQLite recoverLaneKind = "sqlite"
)

// detectRecoverLane sniffs the source content: a PAX/ustar tar header
// magic at offset 257 selects the transfer-tar lane, the SQLite file
// header selects the standalone-snapshot lane.
func detectRecoverLane(source string) (recoverLaneKind, error) {
	file, err := os.Open(source)
	if err != nil {
		return "", fmt.Errorf("opening source archive: %w", err)
	}
	defer file.Close()
	head := make([]byte, tarHeaderMagicOffset+8)
	read, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", fmt.Errorf("reading source archive header: %w", err)
	}
	head = head[:read]
	if bytes.HasPrefix(head, sqliteHeaderMagic) {
		return recoverLaneSQLite, nil
	}
	if len(head) >= tarHeaderMagicOffset+5 &&
		string(head[tarHeaderMagicOffset:tarHeaderMagicOffset+5]) == "ustar" {
		return recoverLaneTar, nil
	}
	return "", nil
}

// refuseExistingRecoverTarget refuses to overwrite the destination or its
// SQLite sidecars: recovery is additive-only and a stale sidecar next to a
// renamed destination could corrupt it on first open.
func refuseExistingRecoverTarget(dest string) error {
	for _, candidate := range append([]string{dest}, sidecarPaths(dest)...) {
		if _, err := os.Lstat(candidate); err == nil {
			return fmt.Errorf(
				"recovery destination %s already exists; refusing to overwrite "+
					"(recovery is additive-only and never replaces a file)",
				candidate)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf(
				"inspecting recovery destination %s: %w", candidate, err)
		}
	}
	return nil
}

// recoverFromSQLiteFile handles a standalone snapshot. A healthy file is
// copied straight through the online-backup API; a damaged one is
// recovered table by table into a fresh engine-schema destination.
func recoverFromSQLiteFile(
	ctx context.Context, source, tmp string, pageSize int, report *recoverReport,
) error {
	integrity, err := sourceQuickCheck(ctx, source)
	if err != nil {
		return err
	}
	if integrity == "ok" {
		// The file is healthy: a byte-faithful online-backup copy is
		// strictly better than any reconstruction.
		report.Lane = "sqlite-copy"
		report.EntriesTotal = 1
		report.EntriesRecovered = 1
		return remotesync.WriteSQLiteSnapshot(tmp, source)
	}

	report.Lane = "sqlite-rows"
	report.EntriesTotal = 1
	report.recordSkip(
		filepath.Base(source), 0,
		"standalone snapshot failed PRAGMA quick_check ("+integrity+
			"); recovering readable rows only")
	return recoverRowsIntoFreshDest(
		ctx, source, tmp, pageSize, report, nil)
}

// sourceQuickCheck runs PRAGMA quick_check on a read-only connection and
// joins the findings. Corrupt pages can echo arbitrary source bytes into
// the finding text, so the result is coerced to valid UTF-8 before it can
// reach a report.
func sourceQuickCheck(ctx context.Context, path string) (string, error) {
	database, err := sql.Open("sqlite3", doctorReadOnlyDSN(path))
	if err != nil {
		return "", fmt.Errorf("opening %s read-only: %w", path, err)
	}
	defer func() { err = errors.Join(err, database.Close()) }()
	integrity, err := quickCheckResult(ctx, database)
	if err != nil {
		return "", fmt.Errorf("running PRAGMA quick_check on %s: %w", path, err)
	}
	return strings.ToValidUTF8(integrity, string(utf8.RuneError)), nil
}

// recoverRowsIntoFreshDest builds the fresh engine-schema destination at
// tmp and imports the readable rows of the SQLite source at sourcePath
// (empty for the tar lane, whose rows arrive through visit while walking
// the tar). The destination is left in DELETE journal mode as one
// self-contained file.
func recoverRowsIntoFreshDest(
	ctx context.Context, sourcePath, tmp string, pageSize int,
	report *recoverReport, visit func(context.Context, *sql.DB) error,
) (err error) {
	if err := createEmptyRecoverDest(tmp); err != nil {
		return err
	}
	engine, err := db.OpenFreshIsolatedContext(ctx, tmp)
	if err != nil {
		return fmt.Errorf("initializing fresh recovery destination: %w", err)
	}
	if err := engine.CloseContext(ctx); err != nil {
		return fmt.Errorf("closing fresh recovery destination: %w", err)
	}

	dest, err := sql.Open("sqlite3", recoverDestDSN(tmp))
	if err != nil {
		return fmt.Errorf("reopening recovery destination: %w", err)
	}
	defer func() {
		closeErr := dest.Close()
		if closeErr != nil {
			_ = os.Remove(tmp)
		}
		err = errors.Join(err, closeErr)
	}()
	if _, err := dest.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS recover_artifacts (
			id                 INTEGER PRIMARY KEY,
			name               TEXT NOT NULL,
			archive_offset     INTEGER NOT NULL,
			size               INTEGER NOT NULL,
			sha256             TEXT NOT NULL,
			kind               TEXT NOT NULL,
			sqlite_quick_check TEXT,
			content            BLOB
		)`,
	); err != nil {
		return fmt.Errorf("creating recovery ledger in destination: %w", err)
	}
	counters := newRecoverCounters()
	if sourcePath != "" {
		if err := importSQLiteRows(
			ctx, sourcePath, dest, pageSize, counters,
		); err != nil {
			return err
		}
	}
	report.RowsPerTable = counters.rowsPerTable
	report.RowSkips = counters.rowSkips
	report.TableSkips = counters.tableSkips
	report.Breakpoints = counters.breakpoints
	if visit != nil {
		if err := visit(ctx, dest); err != nil {
			return err
		}
	}
	if _, err := dest.ExecContext(ctx, `PRAGMA journal_mode = DELETE`); err != nil {
		return fmt.Errorf("finalizing recovery destination: %w", err)
	}
	return nil
}

// createEmptyRecoverDest creates the empty regular file that
// db.OpenFreshIsolatedContext requires.
func createEmptyRecoverDest(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("creating recovery destination %s: %w", path, err)
	}
	return file.Close()
}

// recoverDestDSN opens the fresh destination read-write with foreign keys
// off: recovered rows may reference a session whose row was lost to
// corruption, and dropping such rows would discard real archived content,
// while fabricating a parent would fabricate data. Identity is enforced
// per table instead.
func recoverDestDSN(path string) string {
	escaped := strings.NewReplacer(
		"%", "%25",
		"?", "%3F",
		"#", "%23",
	).Replace(path)
	return "file:" + escaped + "?_busy_timeout=5000"
}

// importSQLiteRows walks every readable table of the SQLite database at
// sourcePath (opened read-only) and inserts its rows additively into
// dest: keyset pagination on rowid, first occurrence wins on identity
// conflicts, breakpoints on mid-table read errors, and verbatim DDL for
// tables the destination does not have. Virtual (FTS) tables and their
// shadow tables are never recovered; they are derived state.
func importSQLiteRows(
	ctx context.Context, sourcePath string, dest *sql.DB, pageSize int,
	counters *recoverCounters,
) (err error) {
	source, err := sql.Open("sqlite3", doctorReadOnlyDSN(sourcePath))
	if err != nil {
		return fmt.Errorf("opening recovered database read-only: %w", err)
	}
	defer func() { err = errors.Join(err, source.Close()) }()

	tables, err := listSourceTables(ctx, source)
	if err != nil {
		return err
	}
	destTables, err := listDestTables(ctx, dest)
	if err != nil {
		return err
	}
	for _, table := range tables {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := recoverTable(
			ctx, source, dest, table, destTables, pageSize, counters,
		); err != nil {
			return err
		}
	}
	return nil
}

// sourceTable is one table discovered in a recovered database.
type sourceTable struct {
	name    string
	ddl     string
	virtual bool
}

// listSourceTables enumerates the real tables of a recovered database in
// deterministic name order. Shadow tables of virtual tables (the FTS
// %_data/%_idx/%_config family) are treated as virtual too: they are
// derived index state, not content.
func listSourceTables(
	ctx context.Context, source *sql.DB,
) ([]sourceTable, error) {
	rows, err := source.QueryContext(ctx,
		`SELECT name, sql FROM sqlite_master
		 WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, fmt.Errorf("listing recovered tables: %w", err)
	}
	defer rows.Close()
	var tables []sourceTable
	for rows.Next() {
		var name string
		var ddl sql.NullString
		if err := rows.Scan(&name, &ddl); err != nil {
			return nil, fmt.Errorf("listing recovered tables: %w", err)
		}
		tables = append(tables, sourceTable{
			name:    name,
			ddl:     ddl.String,
			virtual: strings.Contains(strings.ToUpper(ddl.String), "CREATE VIRTUAL TABLE"),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing recovered tables: %w", err)
	}
	// Shadow tables share their virtual parent's name prefix. Matching on
	// the prefix keeps the pair together without hardcoding FTS naming.
	var real []sourceTable
	for _, table := range tables {
		shadow := false
		if !table.virtual {
			for _, other := range tables {
				if other.virtual && strings.HasPrefix(table.name, other.name+"_") {
					shadow = true
					break
				}
			}
		}
		if !shadow {
			real = append(real, table)
		}
	}
	slices.SortFunc(real, func(a, b sourceTable) int {
		return strings.Compare(a.name, b.name)
	})
	return real, nil
}

// listDestTables returns the table names that already exist in the
// destination.
func listDestTables(
	ctx context.Context, dest *sql.DB,
) (map[string]struct{}, error) {
	rows, err := dest.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return nil, fmt.Errorf("listing destination tables: %w", err)
	}
	defer rows.Close()
	tables := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("listing destination tables: %w", err)
		}
		tables[name] = struct{}{}
	}
	return tables, rows.Err()
}

// recoverTable imports one source table. Every failure mode is contained:
// a corrupt page stops only this table (breakpoint), a constraint
// violation stops only that row, and a schema mismatch stops only the
// table. The destination is never reshaped to fit damaged data.
func recoverTable(
	ctx context.Context, source, dest *sql.DB, table sourceTable,
	destTables map[string]struct{}, pageSize int,
	counters *recoverCounters,
) error {
	if table.virtual {
		counters.tableSkips = append(counters.tableSkips, recoverTableSkip{
			Table: table.name,
			Reason: "virtual table (derived state; a writable open rebuilds " +
				"it from the recovered rows)",
		})
		return nil
	}
	if strings.HasPrefix(table.name, recoverReservedPrefix) {
		counters.tableSkips = append(counters.tableSkips, recoverTableSkip{
			Table:  table.name,
			Reason: "name reserved for recovery bookkeeping",
		})
		return nil
	}
	sourceCols, hasRowid, err := describeSourceTable(ctx, source, table.name)
	if err != nil {
		counters.tableSkips = append(counters.tableSkips, recoverTableSkip{
			Table: table.name, Reason: err.Error(),
		})
		return nil
	}
	_, engineTable := destTables[table.name]
	if engineTable {
		destCols, err := tableColumns(ctx, dest, table.name)
		if err != nil {
			counters.tableSkips = append(counters.tableSkips, recoverTableSkip{
				Table: table.name, Reason: err.Error(),
			})
			return nil
		}
		// Named-column inserts make declaration order irrelevant; the
		// column name sets must still agree exactly, because a partial
		// row would fabricate values the source never had.
		if !slices.Equal(
			slices.Sorted(slices.Values(sourceCols)),
			slices.Sorted(slices.Values(destCols)),
		) {
			counters.tableSkips = append(counters.tableSkips, recoverTableSkip{
				Table: table.name,
				Reason: fmt.Sprintf(
					"columns differ from the destination schema "+
						"(source: %s; destination: %s)",
					strings.Join(sourceCols, ", "),
					strings.Join(destCols, ", ")),
			})
			return nil
		}
	} else {
		// The destination does not know this table: mirror the source's
		// own DDL verbatim so its rows keep their declared identity.
		// Only plain rowid tables are mirrored; anything else cannot be
		// reproduced without interpreting the source schema.
		if !hasRowid {
			counters.tableSkips = append(counters.tableSkips, recoverTableSkip{
				Table: table.name,
				Reason: "not present in the destination schema and has no " +
					"rowid to paginate; not mirrored",
			})
			return nil
		}
		if _, err := dest.ExecContext(ctx, table.ddl); err != nil {
			counters.tableSkips = append(counters.tableSkips, recoverTableSkip{
				Table: table.name,
				Reason: "not present in the destination schema and its DDL " +
					"could not be mirrored verbatim: " + err.Error(),
			})
			return nil
		}
	}

	quotedCols := make([]string, len(sourceCols))
	placeholders := make([]string, len(sourceCols))
	for i, col := range sourceCols {
		quotedCols[i] = quoteSQLiteIdent(col)
		placeholders[i] = "?"
	}
	insertSQL := fmt.Sprintf(
		`INSERT INTO %s (%s) VALUES (%s)`,
		quoteSQLiteIdent(table.name),
		strings.Join(quotedCols, ", "),
		strings.Join(placeholders, ", "),
	)
	pkCols := primaryKeyColumns(ctx, table.name, sourceCols, dest)
	inserted, err := paginateRecoverRows(
		ctx, source, dest, table.name, hasRowid, sourceCols,
		insertSQL, pkCols, pageSize, counters,
	)
	if err != nil {
		return fmt.Errorf("recovering table %s: %w", table.name, err)
	}
	if inserted > 0 {
		counters.rowsPerTable[table.name] = inserted
	}
	return nil
}

// describeSourceTable returns the column names of a source table and
// whether it is a rowid table (probed by selecting rowid).
func describeSourceTable(
	ctx context.Context, source *sql.DB, table string,
) ([]string, bool, error) {
	cols, err := tableColumns(ctx, source, table)
	if err != nil {
		return nil, false, err
	}
	probe, err := source.QueryContext(
		ctx, `SELECT rowid FROM `+quoteSQLiteIdent(table)+` LIMIT 1`)
	if err != nil {
		return cols, false, nil
	}
	return cols, true, probe.Close()
}

// tableColumns reads a table's column names in declared order.
func tableColumns(
	ctx context.Context, database *sql.DB, table string,
) ([]string, error) {
	rows, err := database.QueryContext(
		ctx, `PRAGMA table_info(`+quoteSQLiteIdent(table)+`)`)
	if err != nil {
		return nil, fmt.Errorf("describing table %s: %w", table, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("describing table %s: %w", table, err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("describing table %s: %w", table, err)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("table %s has no readable columns", table)
	}
	return cols, nil
}

// primaryKeyColumns returns the declared primary-key columns of a
// destination table, for the recorded identity key of skipped rows.
func primaryKeyColumns(
	ctx context.Context, table string, sourceCols []string, dest *sql.DB,
) []string {
	rows, err := dest.QueryContext(
		ctx, `PRAGMA table_info(`+quoteSQLiteIdent(table)+`)`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var pkCols []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil
		}
		if pk > 0 {
			pkCols = append(pkCols, name)
		}
	}
	if rows.Err() != nil {
		return nil
	}
	// Only keep the key when every key column is among the imported
	// columns; otherwise the recorded key would be incomplete.
	for _, col := range pkCols {
		if !slices.Contains(sourceCols, col) {
			return nil
		}
	}
	return pkCols
}

// paginateRecoverRows walks a table with rowid keyset pagination (or one
// ordered full pass when the table has no rowid), inserting each readable
// row. A read error mid-table records the breakpoint and stops this table
// only; a constraint violation keeps the first occurrence and records the
// rest. Returns the number of rows inserted.
func paginateRecoverRows(
	ctx context.Context, source, dest *sql.DB,
	table string, hasRowid bool, cols []string,
	insertSQL string, pkCols []string, pageSize int,
	counters *recoverCounters,
) (int64, error) {
	quoted := make([]string, len(cols))
	for i, col := range cols {
		quoted[i] = quoteSQLiteIdent(col)
	}
	selectSQL := fmt.Sprintf(
		`SELECT %s FROM %s`,
		strings.Join(quoted, ", "), quoteSQLiteIdent(table),
	)
	var inserted int64
	var lastRowID int64
	pageArgs := []any{}
	if hasRowid {
		selectSQL = `SELECT rowid, ` + strings.Join(quoted, ", ") +
			` FROM ` + quoteSQLiteIdent(table) +
			` WHERE rowid > ? ORDER BY rowid ASC LIMIT ?`
		pageArgs = []any{lastRowID, pageSize}
	}

	for {
		rows, err := source.QueryContext(ctx, selectSQL, pageArgs...)
		if err != nil {
			counters.breakpoints = append(counters.breakpoints, recoverBreakpoint{
				Table: table, LastRowID: lastRowID, RowsSoFar: inserted,
				Reason: recoverPrintable("read failed: " + err.Error()),
			})
			return inserted, nil
		}
		scanCount := len(cols)
		if hasRowid {
			scanCount++
		}
		values := make([]any, scanCount)
		scan := make([]any, scanCount)
		for i := range values {
			values[i] = new(any)
			scan[i] = values[i]
		}
		pageRows := 0
		for rows.Next() {
			if err := rows.Scan(scan...); err != nil {
				counters.breakpoints = append(counters.breakpoints, recoverBreakpoint{
					Table: table, LastRowID: lastRowID, RowsSoFar: inserted,
					Reason: recoverPrintable("row decode failed: " + err.Error()),
				})
				_ = rows.Close()
				return inserted, nil
			}
			rowValues := make([]any, len(cols))
			offset := 0
			if hasRowid {
				lastRowID = (*values[0].(*any)).(int64)
				offset = 1
			}
			for i := range cols {
				rowValues[i] = *values[i+offset].(*any)
			}
			pageRows++
			if _, err := dest.ExecContext(ctx, insertSQL, rowValues...); err != nil {
				reason := constraintSkipReason(err)
				if reason == "" {
					// An unexpected insert failure is treated like a
					// breakpoint: the rows after it in this table stay
					// unrecovered rather than guessed around.
					counters.breakpoints = append(counters.breakpoints, recoverBreakpoint{
						Table: table, LastRowID: lastRowID, RowsSoFar: inserted,
						Reason: recoverPrintable("insert failed: " + err.Error()),
					})
					_ = rows.Close()
					return inserted, nil
				}
				counters.rowSkips = append(counters.rowSkips, recoverRowSkip{
					Table:       table,
					SourceRowID: lastRowID,
					Key:         rowKey(pkCols, cols, rowValues),
					Reason:      recoverPrintable(reason),
				})
				continue
			}
			inserted++
		}
		if err := rows.Err(); err != nil {
			// The cursor hit unreadable pages: everything before the last
			// good row of this page is already inserted; the rest of the
			// table stays unrecovered. The next table is unaffected.
			counters.breakpoints = append(counters.breakpoints, recoverBreakpoint{
				Table: table, LastRowID: lastRowID, RowsSoFar: inserted,
				Reason: recoverPrintable("read failed: " + err.Error()),
			})
			_ = rows.Close()
			return inserted, nil
		}
		if err := rows.Close(); err != nil {
			return inserted, fmt.Errorf(
				"finishing read of table %s: %w", table, err)
		}
		if !hasRowid || pageRows == 0 {
			// The no-rowid single ordered pass is the whole table; an
			// empty page means the keyset is exhausted.
			return inserted, nil
		}
		pageArgs = []any{lastRowID, pageSize}
	}
}

// rowKey renders the primary-key columns of a recovered row for the
// skip ledger.
func rowKey(pkCols, cols []string, rowValues []any) string {
	if len(pkCols) == 0 {
		return ""
	}
	var parts []string
	for _, pk := range pkCols {
		i := slices.Index(cols, pk)
		if i < 0 {
			continue
		}
		parts = append(parts,
			fmt.Sprintf("%s=%v", pk, recoverPrintableValue(rowValues[i])))
	}
	return strings.Join(parts, ",")
}

// recoverPrintableValue renders one column value for the skip ledger.
func recoverPrintableValue(value any) string {
	switch typed := value.(type) {
	case []byte:
		return recoverPrintable(string(typed))
	case string:
		return recoverPrintable(typed)
	default:
		return fmt.Sprintf("%v", value)
	}
}

// constraintSkipReason classifies an insert failure. Constraint
// violations are identity conflicts (the first occurrence of a key stays
// authoritative) or contract violations (the row cannot be stored as
// declared); both are recorded and skipped, never rewritten. Any other
// failure returns "" so the caller treats it as a table breakpoint.
func constraintSkipReason(err error) string {
	var sqliteErr sqlite3.Error
	if !errors.As(err, &sqliteErr) ||
		sqliteErr.Code&sqlite3.ErrNo(sqlite3.ErrNoMask) != sqlite3.ErrConstraint {
		return ""
	}
	switch sqliteErr.ExtendedCode {
	case sqlite3.ErrConstraintPrimaryKey, sqlite3.ErrConstraintUnique:
		return "identity conflict: a row with this primary key was already " +
			"recovered; the first occurrence is kept"
	default:
		return "constraint violation: " + sqliteErr.Error()
	}
}

// quoteSQLiteIdent quotes one identifier for SQL text.
func quoteSQLiteIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

// countingReader tracks how many bytes the tar walker consumed, so skip
// records can point at the stream position where damage was met.
type countingReader struct {
	r     io.Reader
	total int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.total += int64(n)
	return n, err
}

// recoverFromTar handles a transfer tar. Recovery is lenient where sync
// extraction is fail-closed: a missing tar end marker is fine, and every
// entry that parses fully before the first structural damage is
// recovered. Damage ends the walk — recovery yields the readable prefix
// and never guesses past it.
func recoverFromTar(
	ctx context.Context, source, tmp string, pageSize int, report *recoverReport,
) error {
	report.Lane = "tar"
	return recoverRowsIntoFreshDest(ctx, "", tmp, pageSize, report,
		func(ctx context.Context, dest *sql.DB) error {
			return walkRecoverTar(ctx, source, dest, pageSize, report)
		})
}

// walkRecoverTar streams the tar leniently, stores every fully parsed
// entry verbatim in the recover_artifacts ledger, and row-imports the
// SQLite entries that pass quick_check.
func walkRecoverTar(
	ctx context.Context, source string, dest *sql.DB,
	pageSize int, report *recoverReport,
) (err error) {
	file, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("opening transfer archive: %w", err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()

	counting := &countingReader{r: file}
	reader := tar.NewReader(counting)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entryOffset := counting.total
		header, hdrErr := reader.Next()
		if hdrErr != nil {
			// Lenient end-of-archive: io.EOF is accepted with or without
			// the 1024-byte end marker (the fail-closed sync extractor
			// requires the marker; recovery does not). Any other error is
			// the first structural damage: record it and stop — recovery
			// yields the readable prefix and never guesses past damage.
			if errors.Is(hdrErr, io.EOF) {
				return nil
			}
			report.recordSkip("", entryOffset,
				"tar stream ended at the first unreadable region: "+
					hdrErr.Error())
			return nil
		}
		report.EntriesTotal++
		kind := recoverEntryKind(header.Typeflag)
		if header.Typeflag != tar.TypeReg {
			// Directories and links carry no content; record them so the
			// recovered inventory is complete.
			if err := recordRecoverArtifact(
				ctx, dest, header.Name, entryOffset, 0, kind, "", nil,
			); err != nil {
				return err
			}
			report.EntriesRecovered++
			continue
		}
		content, readErr := io.ReadAll(reader)
		if readErr != nil {
			// A truncated entry is never completed from elsewhere and its
			// partial bytes are never presented as the file: skip and
			// record. The walk cannot continue past it.
			report.recordSkip(header.Name, entryOffset,
				fmt.Sprintf("entry content truncated after %d of %d bytes: %v",
					len(content), header.Size, readErr))
			return nil
		}
		sqliteEntry := len(content) >= 100 &&
			bytes.HasPrefix(content, sqliteHeaderMagic)
		quickCheck := ""
		if sqliteEntry {
			quickCheck = "pending"
		}
		if err := recordRecoverArtifact(
			ctx, dest, header.Name, entryOffset, int64(len(content)), kind,
			quickCheck, content,
		); err != nil {
			return err
		}
		report.EntriesRecovered++
		if !sqliteEntry {
			continue
		}
		// The entry claims to be a SQLite database: verify before any row
		// is imported. Corrupt databases are recorded and skipped, never
		// fabricated from their readable fragments.
		integrity, err := quickCheckScratchSQLite(ctx, content)
		if err != nil {
			report.recordSkip(header.Name, entryOffset,
				"sqlite entry could not be opened for verification: "+
					err.Error())
			continue
		}
		if err := updateRecoverArtifactQuickCheck(
			ctx, dest, header.Name, entryOffset, integrity,
		); err != nil {
			return err
		}
		if integrity != "ok" {
			report.recordSkip(header.Name, entryOffset,
				"sqlite entry failed PRAGMA quick_check ("+integrity+
					"); content preserved in recover_artifacts, rows not imported")
			continue
		}
		scratch, err := writeRecoverScratch(content)
		if err != nil {
			return err
		}
		counters := newRecoverCounters()
		importErr := importSQLiteRows(
			ctx, scratch, dest, pageSize, counters)
		_ = os.Remove(scratch)
		if importErr != nil {
			return importErr
		}
		mergeCounters(report, counters)
	}
}

// mergeCounters folds one entry's row accounting into the report.
func mergeCounters(report *recoverReport, counters *recoverCounters) {
	for table, rows := range counters.rowsPerTable {
		report.RowsPerTable[table] += rows
	}
	report.RowSkips = append(report.RowSkips, counters.rowSkips...)
	report.TableSkips = append(report.TableSkips, counters.tableSkips...)
	report.Breakpoints = append(report.Breakpoints, counters.breakpoints...)
}

// recoverEntryKind classifies a tar entry for the ledger.
func recoverEntryKind(typeflag byte) string {
	switch typeflag {
	case tar.TypeReg:
		return "file"
	case tar.TypeDir:
		return "dir"
	case tar.TypeSymlink:
		return "symlink"
	case tar.TypeLink:
		return "hardlink"
	default:
		return "other"
	}
}

// recordRecoverArtifact stores one fully parsed entry verbatim. The bytes
// are the entry's own content, hashed as written: recovery preserves
// evidence, it does not reconstruct.
func recordRecoverArtifact(
	ctx context.Context, dest *sql.DB, name string, offset, size int64,
	kind, quickCheck string, content []byte,
) error {
	digest := sha256.Sum256(content)
	_, err := dest.ExecContext(ctx,
		`INSERT INTO recover_artifacts
		 (name, archive_offset, size, sha256, kind, sqlite_quick_check, content)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		name, offset, size, hex.EncodeToString(digest[:]), kind,
		sqlNullString(quickCheck), content,
	)
	if err != nil {
		return fmt.Errorf("recording recovered entry %q: %w", name, err)
	}
	return nil
}

// updateRecoverArtifactQuickCheck stores the quick_check verdict for one
// entry once the scratch verification finished.
func updateRecoverArtifactQuickCheck(
	ctx context.Context, dest *sql.DB, name string, offset int64, quickCheck string,
) error {
	_, err := dest.ExecContext(ctx,
		`UPDATE recover_artifacts SET sqlite_quick_check = ?
		 WHERE name = ? AND archive_offset = ?`,
		quickCheck, name, offset,
	)
	if err != nil {
		return fmt.Errorf("recording quick_check for %q: %w", name, err)
	}
	return nil
}

func sqlNullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// quickCheckScratchSQLite verifies in-memory entry content by writing it
// to a scratch file and running PRAGMA quick_check read-only.
func quickCheckScratchSQLite(ctx context.Context, content []byte) (string, error) {
	scratch, err := writeRecoverScratch(content)
	if err != nil {
		return "", err
	}
	defer os.Remove(scratch)
	return sourceQuickCheck(ctx, scratch)
}

// writeRecoverScratch writes entry content to a scratch file so the
// SQLite driver can open it. The caller removes the file.
func writeRecoverScratch(content []byte) (string, error) {
	file, err := os.CreateTemp("", "agentsview-recover-*.db")
	if err != nil {
		return "", fmt.Errorf("creating scratch file for recovered entry: %w", err)
	}
	path := file.Name()
	if _, err := file.Write(content); err != nil {
		file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("writing scratch file for recovered entry: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("writing scratch file for recovered entry: %w", err)
	}
	return path, nil
}

func writeRecoverReportJSON(w io.Writer, report *recoverReport) error {
	return json.MarshalEncode(
		jsontext.NewEncoder(w, jsontext.WithIndent("  ")), report)
}

func writeRecoverReportHuman(w io.Writer, report *recoverReport) error {
	fmt.Fprintln(w, "Recovery complete.")
	fmt.Fprintf(w, "  Source:       %s (opened read-only)\n", report.Source)
	fmt.Fprintf(w, "  Destination:  %s (fresh archive, additive-only)\n",
		report.Destination)
	fmt.Fprintf(w, "  Lane:         %s\n", report.Lane)
	fmt.Fprintf(w, "  Entries:      %d recovered of %d\n",
		report.EntriesRecovered, report.EntriesTotal)
	if len(report.RowsPerTable) > 0 {
		fmt.Fprintf(w, "  Rows recovered per table:\n")
		for _, table := range slices.Sorted(maps.Keys(report.RowsPerTable)) {
			fmt.Fprintf(w, "    %-40s %d\n", table, report.RowsPerTable[table])
		}
	}
	for _, skip := range report.EntriesSkipped {
		fmt.Fprintf(w, "  Skipped entry: %s at offset %d: %s\n",
			skip.Name, skip.Offset, skip.Reason)
	}
	for _, tableSkip := range report.TableSkips {
		fmt.Fprintf(w, "  Skipped table: %s: %s\n",
			tableSkip.Table, tableSkip.Reason)
	}
	for _, rowSkip := range report.RowSkips {
		fmt.Fprintf(w, "  Skipped row:   %s %s (source rowid %d): %s\n",
			rowSkip.Table, rowSkip.Key, rowSkip.SourceRowID, rowSkip.Reason)
	}
	for _, bp := range report.Breakpoints {
		fmt.Fprintf(w, "  Breakpoint:    %s after rowid %d (%d rows): %s\n",
			bp.Table, bp.LastRowID, bp.RowsSoFar, bp.Reason)
	}
	fmt.Fprintf(w, "  Content:      recovered entry bytes are stored verbatim "+
		"in the destination's recover_artifacts table\n")
	return nil
}
