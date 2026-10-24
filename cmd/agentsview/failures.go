// ABOUTME: `agentsview failures` — read-only listing of the source files
// ABOUTME: whose parse failures sync persists, with each entry's derived
// ABOUTME: retry disposition.
package main

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/sync"
	"go.kenn.io/agentsview/internal/sync/failurecache"
)

func newFailuresCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "failures",
		Short: "List source files whose parse failed and their retry state",
		Long: "List the source files whose last parse failed and that sync skips\n" +
			"until they change. Read-only: this command never runs a sync and\n" +
			"never modifies the archive.\n\n" +
			"Each row shows the agent and source path, the failure class the\n" +
			"engine recorded (malformed-json, shape-violation, unsupported-version,\n" +
			"parse-failed, or source-missing when the file did not exist), how many\n" +
			"of the bounded parse attempts the recorded identity has spent on that\n" +
			"class, the identity recorded at failure time (file mtime and size),\n" +
			"and the disposition the next sync will apply: waiting while the file\n" +
			"is unchanged, due for retry once its stats changed or it came back,\n" +
			"or missing while the file is absent. A source whose attempt budget is\n" +
			"exhausted stays skipped until its stats or failure class change or a\n" +
			"parser upgrade lands. The engine re-verifies the fingerprint half of\n" +
			"the identity at sync time, so a waiting row is advisory, not a\n" +
			"guarantee of a skip.",
		GroupID:      groupData,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadReadOnly()
			if err != nil {
				return err
			}
			database, err := openReadOnlyDB(cmd.Context(), cfg)
			if err != nil {
				return fmt.Errorf("opening archive read-only: %w", err)
			}
			defer database.Close()
			rows, err := collectFailureRows(cmd.Context(), database)
			if err != nil {
				return err
			}
			if outputFormat(cmd) == "json" {
				return writeFailuresJSON(cmd.OutOrStdout(), rows)
			}
			return writeFailuresHuman(cmd.OutOrStdout(), rows)
		},
	}
	registerFormatFlags(cmd.Flags())
	return cmd
}

// failureRow is one persisted source failure prepared for display.
type failureRow struct {
	CacheKey    string
	Agent       string
	Source      string
	Class       string
	Recorded    db.SourceFailure
	Disposition failurecache.Disposition
	// Attempts is the bounded retry budget the recorded identity has spent
	// on its failure class, when the engine persisted a quarantine ledger
	// for the row. ok is false for rows without one.
	Attempts    int
	HasAttempts bool
}

// failureClass labels which kind of failure the recorded identity captures,
// falling back to the coarse pre-quarantine labels when the engine recorded
// no failure class for the row.
func failureClass(recorded db.SourceFailure, durable string) string {
	if durable != "" {
		return durable
	}
	if recorded.Missing {
		return "source-missing"
	}
	return "parse-failed"
}

// dispositionLabel expands a derived disposition into the human wording of
// the report. The JSON payload carries the same strings so both outputs stay
// comparable.
func dispositionLabel(d failurecache.Disposition) string {
	switch d {
	case failurecache.DispositionWaiting:
		return "waiting (unchanged since failure)"
	case failurecache.DispositionRetryStatsChanged:
		return "due for retry (stats changed)"
	case failurecache.DispositionRetryReturned:
		return "due for retry (file returned)"
	case failurecache.DispositionMissing:
		return "missing (skip until it returns)"
	default:
		return "unknown (decided at sync time)"
	}
}

// agentFromSkipSuffix extracts the agent qualifier a skip-cache key carries
// after its path, mirroring the qualifier remote import reattaches.
func agentFromSkipSuffix(suffix string) string {
	agent := strings.TrimPrefix(suffix, "?agent=")
	if before, _, ok := strings.Cut(agent, "?"); ok {
		agent = before
	}
	return agent
}

// collectFailureRows loads the persisted source failures and derives each
// entry's disposition from the current filesystem. Rows sort by the recorded
// file mtime, newest first — the closest stored signal to when the failure
// was last seen — with the cache key breaking ties so output is stable.
func collectFailureRows(
	ctx context.Context, database *db.DB,
) ([]failureRow, error) {
	entries, extras, err := database.LoadSourceFailureDetails(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading source failures: %w", err)
	}
	rows := make([]failureRow, 0, len(entries))
	for key, recorded := range entries {
		extra := extras[key]
		path, suffix := sync.SplitProviderSkipCachePath(key)
		hasAttempts := extra != db.SourceFailureExtra{} &&
			failurecache.Class(extra.Class).QuarantineEligible()
		rows = append(rows, failureRow{
			CacheKey:    key,
			Agent:       agentFromSkipSuffix(suffix),
			Source:      path,
			Class:       failureClass(recorded, extra.Class),
			Recorded:    recorded,
			Disposition: failurecache.Classify(path, recorded),
			Attempts:    extra.Attempts,
			HasAttempts: hasAttempts,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Recorded.MTimeNS != rows[j].Recorded.MTimeNS {
			return rows[i].Recorded.MTimeNS > rows[j].Recorded.MTimeNS
		}
		return rows[i].CacheKey < rows[j].CacheKey
	})
	return rows, nil
}

func writeFailuresHuman(w io.Writer, rows []failureRow) error {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No source failures recorded.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tCLASS\tATTEMPTS\tSOURCE\tRECORDED MTIME\tSIZE\tDISPOSITION")
	for _, row := range rows {
		agent := row.Agent
		if agent == "" {
			agent = "(unknown)"
		}
		mtime := emDash
		if row.Recorded.MTimeNS != 0 {
			mtime = time.Unix(0, row.Recorded.MTimeNS).UTC().Format(time.RFC3339)
		}
		attempts := emDash
		if row.HasAttempts {
			attempts = fmtInt64(int64(row.Attempts))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s B\t%s\n",
			agent, row.Class, attempts, row.Source, mtime,
			fmtInt64(row.Recorded.Size), dispositionLabel(row.Disposition))
	}
	return tw.Flush()
}

// failureJSON is the machine-readable form of a failureRow. The recorded
// mtime is omitted for source-missing entries, which record none. The
// attempts field is omitted for rows without a quarantine ledger.
type failureJSON struct {
	Agent             string `json:"agent"`
	Source            string `json:"source"`
	Class             string `json:"class"`
	Attempts          int    `json:"attempts,omitzero"`
	RecordedMTime     string `json:"recorded_mtime,omitempty"`
	RecordedSizeBytes int64  `json:"recorded_size_bytes"`
	Fingerprint       string `json:"recorded_fingerprint,omitempty"`
	Disposition       string `json:"disposition"`
}

func writeFailuresJSON(w io.Writer, rows []failureRow) error {
	payload := make([]failureJSON, 0, len(rows))
	for _, row := range rows {
		entry := failureJSON{
			Agent:             row.Agent,
			Source:            row.Source,
			Class:             row.Class,
			RecordedSizeBytes: row.Recorded.Size,
			Fingerprint:       row.Recorded.Fingerprint,
			Disposition:       dispositionLabel(row.Disposition),
		}
		if row.HasAttempts {
			entry.Attempts = row.Attempts
		}
		if row.Recorded.MTimeNS != 0 {
			entry.RecordedMTime = time.Unix(0, row.Recorded.MTimeNS).UTC().Format(time.RFC3339)
		}
		payload = append(payload, entry)
	}
	return json.MarshalEncode(jsontext.NewEncoder(w, jsontext.WithIndent("  ")), payload)
}
