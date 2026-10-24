package parser

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

var _ Provider = (*antigravityProvider)(nil)

type antigravityProviderFactory struct {
	def AgentDef
}

func newAntigravityProviderFactory(def AgentDef) ProviderFactory {
	return antigravityProviderFactory{def: cloneAgentDef(def)}
}

func (f antigravityProviderFactory) Definition() AgentDef {
	return cloneAgentDef(f.def)
}

func (f antigravityProviderFactory) Capabilities() Capabilities {
	return antigravityProviderCapabilities()
}

func (f antigravityProviderFactory) NewProvider(cfg ProviderConfig) Provider {
	cfg = cfg.Clone()
	return &antigravityProvider{
		Def:     cloneAgentDef(f.def),
		Caps:    antigravityProviderCapabilities(),
		Config:  cfg,
		sources: newAntigravitySourceSet(cfg.Roots),
	}
}

type antigravityProvider struct {
	ProviderBase
	sources antigravitySourceSet
}

func (p *antigravityProvider) Discover(ctx context.Context) ([]SourceRef, error) {
	return p.sources.Discover(ctx)
}

func (p *antigravityProvider) DiscoverEach(ctx context.Context, yield func(SourceRef) error) error {
	return p.sources.DiscoverEach(ctx, yield)
}

func (p *antigravityProvider) WatchPlan(ctx context.Context) (WatchPlan, error) {
	return p.sources.WatchPlan(ctx)
}

func (p *antigravityProvider) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	return p.sources.SourcesForChangedPath(ctx, req)
}

func (p *antigravityProvider) ChangedPathRelevance(
	ctx context.Context,
	req ChangedPathRequest,
) (ChangedPathRelevance, error) {
	if err := ctx.Err(); err != nil {
		return ChangedPathUnclassified, err
	}
	for _, root := range p.sources.roots {
		if req.WatchRoot != "" && !antigravityWatchRootMatches(root, req.WatchRoot) {
			continue
		}
		rel, ok := relUnder(root, filepath.Clean(req.Path))
		if ok && (antigravityConversationIndexPath(rel) ||
			rel == AntigravitySummariesDBName+"-shm") {
			return ChangedPathNonData, nil
		}
	}
	return ChangedPathUnclassified, nil
}

// antigravityConversationIndexPath reports whether rel names the WAL index of
// a conversation database. Every read-only open rewrites that index without
// changing session data; committed writes land in the main file or the -wal.
func antigravityConversationIndexPath(rel string) bool {
	name := filepath.Base(rel)
	return filepath.Dir(rel) == "conversations" &&
		strings.HasSuffix(name, ".db-shm") &&
		IsValidSessionID(strings.TrimSuffix(name, ".db-shm"))
}

func (p *antigravityProvider) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	req = ProviderFindRequestWithRawSessionID(p.Def, req)
	return p.sources.FindSource(ctx, req)
}

func (p *antigravityProvider) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	return p.sources.Fingerprint(ctx, source)
}

// ComputeMultiFileStatHash implements parser.MultiFileStatHasher for the IDE
// session composite: the conversations/<id>.db plus exactly the companions
// AntigravityFileInfo folds into the effective stat. Each component
// contributes a (size, mtime, ctime, inode) tuple, so a same-size,
// same-mtime rewrite of the source — invisible to the size/mtime skip
// composite — bumps a ctime or swaps an inode and breaks the digest, forcing
// the fingerprint whose content hash then gates the skip
// (FingerprintHashRequiredForFreshness). An unverifiable identity field
// zeroes the digest (see antigravityStatTupleDigest); freshness then rests
// on the content-hash gate alone instead of a composite the platform cannot
// back.
func (p *antigravityProvider) ComputeMultiFileStatHash(chatPath string) uint64 {
	return antigravityStatTupleDigest(antigravityIDEStatDigestSep, chatPath,
		antigravityIDECompanionPaths(chatPath)...)
}

func (p *antigravityProvider) Parse(
	ctx context.Context,
	req ParseRequest,
) (ParseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return ParseOutcome{}, err
	}
	src, ok := p.sources.sourceFromRef(ctx, req.Source)
	if !ok {
		return ParseOutcome{}, errors.New("antigravity source path unavailable")
	}
	if _, err := os.Stat(src.Path); err != nil {
		if os.IsNotExist(err) {
			return ParseOutcome{
				ResultSetComplete: true,
				ForceReplace:      true,
				SkipReason:        SkipNoSession,
			}, nil
		}
		return ParseOutcome{}, fmt.Errorf("stat %s: %w", src.Path, err)
	}
	machine := firstNonEmptyJSONLString(req.Machine, p.Config.Machine)
	sess, msgs, usageEvents, err := p.parseSession(ctx,
		src.Path,
		req.Source.ProjectHint,
		machine,
	)
	if err != nil {
		return ParseOutcome{}, err
	}
	if sess == nil {
		return ParseOutcome{
			ResultSetComplete: true,
			ForceReplace:      true,
			SkipReason:        SkipNoSession,
		}, nil
	}
	applyAntigravityTitle(ctx, sess, src)
	if req.Fingerprint.Hash != "" {
		sess.File.Hash = req.Fingerprint.Hash
	}
	return ParseOutcome{
		Results: []ParseResultOutcome{{
			Result: ParseResult{
				Session:     *sess,
				Messages:    msgs,
				UsageEvents: usageEvents,
			},
			DataVersion: DataVersionCurrent,
		}},
		ResultSetComplete: true,
		ForceReplace:      true,
	}, nil
}

type antigravitySource struct {
	Root string
	Path string
	ID   string
	// SubagentParentID and SubagentRole carry the resolved
	// brain/<parent>/.system_generated/subagents/<id>.json link when one
	// claims this session. In-memory only; lookup fallbacks re-resolve
	// them when the cache missed at ref construction.
	SubagentParentID string
	SubagentRole     string
}

type antigravitySummary struct {
	workspaceURIs string
	title         string
	cwd           string
	project       string
}

// antigravitySummaryDBReader is the database-open hook the summary
// index runs a scan through. It is a package variable so tests can
// count how many times a pass opens the database.
var antigravitySummaryDBReader = WithSQLiteReadOnly

type antigravitySummaryIndex struct {
	mu      sync.RWMutex
	entries map[string]antigravitySummary
	// scannedRoots marks roots whose conversation_summaries.db scan
	// completed -- found, absent, or unreadable alike. On a scanned root
	// an entries miss is definitive, so per-source lookups never reopen
	// the database: one scan's open is shared by every source of the
	// root instead of multiplying the busy-timeout plus scratch-copy
	// cost across the discover pass.
	scannedRoots map[string]struct{}
}

func newAntigravitySummaryIndex() *antigravitySummaryIndex {
	return &antigravitySummaryIndex{
		entries:      make(map[string]antigravitySummary),
		scannedRoots: make(map[string]struct{}),
	}
}

func (x *antigravitySummaryIndex) lookup(
	ctx context.Context, root, id string,
) (antigravitySummary, bool) {
	if x == nil || id == "" {
		return antigravitySummary{}, false
	}
	cleanRoot := filepath.Clean(root)
	key := cleanRoot + "\x00" + id
	x.mu.RLock()
	sum, ok := x.entries[key]
	_, scanned := x.scannedRoots[cleanRoot]
	x.mu.RUnlock()
	if ok || scanned {
		return sum, ok
	}
	// First touch of a root no scan has covered yet (a stored-path or
	// changed-path ref built before any discovery pass): load the whole
	// root once rather than probing the database per source.
	x.scan(ctx, root)
	x.mu.RLock()
	sum, ok = x.entries[key]
	x.mu.RUnlock()
	return sum, ok
}

// scan reloads root's conversation_summaries.db into the index. A scan
// is a complete snapshot: on success it replaces the root's cached rows
// so deleted summaries do not linger, and it always marks the root
// scanned (unless ctx was cancelled) so lookup-triggered rescans stay
// bounded at one database open per root. A failed scan keeps the stale
// rows; the next Discover pass or summaries-file event retries the
// load.
func (x *antigravitySummaryIndex) scan(ctx context.Context, root string) {
	if x == nil || root == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cleanRoot := filepath.Clean(root)
	dbPath := filepath.Join(root, "conversation_summaries.db")
	batch := make(map[string]antigravitySummary)
	// An absent database is a complete (empty) snapshot.
	loaded := true
	if IsRegularFile(dbPath) {
		loaded = antigravitySummaryDBReader(
			ctx, dbPath, sqliteReadOptions{busyTimeoutMS: 3000},
			func(db *sql.DB) error {
				rows, err := db.QueryContext(ctx, "SELECT conversation_id, COALESCE(workspace_uris, ''), COALESCE(title, '') FROM conversation_summaries WHERE conversation_id != ''")
				if err != nil {
					rows, err = db.QueryContext(ctx, "SELECT conversation_id, COALESCE(workspace_uris, '') FROM conversation_summaries WHERE conversation_id != ''")
					if err != nil {
						return err
					}
				}
				defer rows.Close()

				cols, _ := rows.Columns()
				hasTitle := len(cols) >= 3

				for rows.Next() {
					var id, ws, title string
					var scanErr error
					if hasTitle {
						scanErr = rows.Scan(&id, &ws, &title)
					} else {
						scanErr = rows.Scan(&id, &ws)
					}
					if scanErr != nil || id == "" {
						continue
					}
					cwd, project := parseAntigravityWorkspaceURI(ws)
					batch[cleanRoot+"\x00"+id] = antigravitySummary{
						workspaceURIs: ws,
						title:         title,
						cwd:           cwd,
						project:       project,
					}
				}
				return rows.Err()
			},
		) == nil
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if loaded {
		prefix := cleanRoot + "\x00"
		for key := range x.entries {
			if strings.HasPrefix(key, prefix) {
				delete(x.entries, key)
			}
		}
		for key, sum := range batch {
			x.entries[key] = sum
		}
	}
	if ctx.Err() == nil {
		x.scannedRoots[cleanRoot] = struct{}{}
	}
}

func parseAntigravityWorkspaceURI(raw string) (cwd, project string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ""
	}
	var uris []string
	if err := json.Unmarshal([]byte(trimmed), &uris); err == nil && len(uris) > 0 {
		raw = uris[0]
	} else {
		raw = strings.Trim(trimmed, "[]\"' ")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme == "file" {
		cwd = parsed.Path
	} else if strings.HasPrefix(raw, "file://") {
		cwd = strings.TrimPrefix(raw, "file://")
	} else {
		cwd = raw
	}
	if unescaped, err := url.PathUnescape(cwd); err == nil && unescaped != "" {
		cwd = unescaped
	}
	if len(cwd) >= 3 && cwd[0] == '/' && cwd[2] == ':' {
		cwd = cwd[1:]
	}
	cwd = filepath.Clean(cwd)
	if cwd != "" && cwd != "/" && cwd != "." {
		project = filepath.Base(cwd)
	}
	return cwd, project
}

type antigravitySourceSet struct {
	roots     []string
	subagents *antigravitySubagentIndex
	summaries *antigravitySummaryIndex
}

func newAntigravitySourceSet(roots []string) antigravitySourceSet {
	return antigravitySourceSet{
		roots:     cleanJSONLRoots(roots),
		subagents: newAntigravitySubagentIndex(),
		summaries: newAntigravitySummaryIndex(),
	}
}

func (s antigravitySourceSet) Discover(ctx context.Context) ([]SourceRef, error) {
	var sources []SourceRef
	seen := make(map[string]struct{})
	for _, root := range s.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.subagents.scan(root)
		s.summaries.scan(ctx, root)
		for _, path := range s.discoverSessionPaths(root) {
			source, ok := s.sourceRef(ctx, root, path, false)
			if ok {
				addJSONLSource(source, &sources, seen)
			}
		}
	}
	sortJSONLSources(sources)
	return sources, nil
}

func (s antigravitySourceSet) DiscoverEach(ctx context.Context, yield func(SourceRef) error) error {
	for _, root := range s.roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.subagents.scan(root)
		s.summaries.scan(ctx, root)
		dir := filepath.Join(root, "conversations")
		err := streamDirectoryEntries(ctx, dir, func(entry os.DirEntry) error {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".db") ||
				!IsValidSessionID(strings.TrimSuffix(name, ".db")) {
				return nil
			}
			if source, ok := s.sourceRef(ctx, root, filepath.Join(dir, name), false); ok {
				if err := yield(source); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		err = streamDirectoryEntries(ctx, filepath.Join(root, "brain"), func(entry os.DirEntry) error {
			id := entry.Name()
			if !entry.IsDir() || !IsValidSessionID(id) ||
				IsRegularFile(filepath.Join(root, "conversations", id+".db")) {
				return nil
			}
			if source, ok := s.sourceRef(ctx, root, antigravityBrainTranscriptPath(root, id), false); ok {
				return yield(source)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// discoverSessionPaths returns one path per IDE session under root, sorted by
// path: every conversations/<uuid>.db, plus the brain transcript of every
// conversation that has no database of its own. It owns the on-disk discovery
// the package-level DiscoverAntigravitySessions free function used to provide.
func (s antigravitySourceSet) discoverSessionPaths(root string) []string {
	if root == "" {
		return nil
	}
	paths := antigravityBrainTranscriptSources(root)
	dir := filepath.Join(root, "conversations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		slices.Sort(paths)
		return paths
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".db") {
			continue
		}
		id := strings.TrimSuffix(name, ".db")
		if !IsValidSessionID(id) {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	slices.Sort(paths)
	return paths
}

// findSourceFile locates the database or standalone brain transcript for a
// conversation under root. It owns the lookup the package-level
// FindAntigravitySourceFile free function used to provide.
func (s antigravitySourceSet) findSourceFile(root, id string) string {
	if root == "" || !IsValidSessionID(id) {
		return ""
	}
	for _, p := range []string{
		filepath.Join(root, "conversations", id+".db"),
		antigravityBrainTranscriptPath(root, id),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func (s antigravitySourceSet) WatchPlan(context.Context) (WatchPlan, error) {
	roots := make([]WatchRoot, 0, len(s.roots)*4)
	for _, root := range s.roots {
		roots = append(roots,
			WatchRoot{
				Path:         filepath.Join(root, "annotations"),
				Recursive:    false,
				IncludeGlobs: []string{"*.pbtxt"},
				DebounceKey:  string(AgentAntigravity) + ":annotations:" + root,
			},
			// brain/<id>/*.md, the subagent descriptors under
			// brain/<id>/.system_generated/subagents, and the plaintext
			// transcript at
			// brain/<id>/.system_generated/logs/transcript.jsonl are
			// parsed. The other generated trees (.git, scratch, and the
			// rest of .system_generated) stay unwatched so they cannot
			// exhaust the recursive-watch budget. Both generated
			// directories must be ExtraDirectories, not just include
			// globs: at MaxDepth 1 fsnotify installs no watch below
			// .system_generated without them, so a glob alone reports
			// nothing and the descriptor events would never reach
			// SourcesForChangedPath.
			WatchRoot{
				Path:      filepath.Join(root, "brain"),
				Recursive: true,
				MaxDepth:  1,
				ExtraDirectories: []string{
					"*/" + antigravityBrainGeneratedDir + "/" + antigravityBrainLogsDir,
					"*/" + antigravityBrainGeneratedDir + "/" + antigravitySubagentsDir,
				},
				IncludeGlobs: []string{
					"*.md",
					"*.md.metadata.json",
					antigravityBrainTranscriptName,
					"*/" + antigravityBrainGeneratedDir + "/" +
						antigravitySubagentsDir + "/*.json",
					"*/" + antigravityBrainGeneratedDir + "/" +
						antigravityBrainLogsDir + "/" + antigravityBrainTranscriptName,
				},
				DebounceKey: string(AgentAntigravity) + ":brain:" + root,
			},
			WatchRoot{
				Path:         filepath.Join(root, "conversations"),
				Recursive:    false,
				IncludeGlobs: []string{"*.db", "*.db-wal", "*.trajectory.json"},
				DebounceKey:  string(AgentAntigravity) + ":conversations:" + root,
			},
			WatchRoot{
				Path:         root,
				Recursive:    false,
				IncludeGlobs: []string{"conversation_summaries.db*"},
				DebounceKey:  string(AgentAntigravity) + ":summaries:" + root,
			},
		)
	}
	return WatchPlan{Roots: roots}, nil
}

func (s antigravitySourceSet) SourcesForChangedPath(
	ctx context.Context,
	req ChangedPathRequest,
) ([]SourceRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, root := range s.roots {
		if req.WatchRoot != "" && !antigravityWatchRootMatches(root, req.WatchRoot) {
			continue
		}
		if isAntigravitySummaryDB(root, req.Path) {
			s.summaries.scan(ctx, root)
			var sources []SourceRef
			for _, p := range s.discoverSessionPaths(root) {
				if src, ok := s.sourceRef(ctx, root, p, false); ok {
					sources = append(sources, src)
				}
			}
			return sources, nil
		}
		source, ok := s.sourceForChangedPath(ctx, root, req.Path)
		if ok {
			return []SourceRef{source}, nil
		}
	}
	return nil, nil
}

func isAntigravitySummaryDB(root, path string) bool {
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(root)
	if filepath.Dir(cleanPath) != cleanRoot {
		return false
	}
	// Same journal rule as conversations/*.db (see
	// antigravityConversationDBForPath): a bare "-shm" event never
	// resolves, because every summaries scan opens the database
	// read-only and rewrites that index, so honoring it would make each
	// scan schedule the next one in a self-exciting loop. The "-wal"
	// sidecar folds onto the base name: committed writes land there.
	base := strings.TrimSuffix(filepath.Base(cleanPath), "-wal")
	return base == "conversation_summaries.db"
}

func (s antigravitySourceSet) FindSource(
	ctx context.Context,
	req FindSourceRequest,
) (SourceRef, bool, error) {
	if err := ctx.Err(); err != nil {
		return SourceRef{}, false, err
	}
	freshStoredSource := req.RequireFreshSource &&
		(req.StoredFilePath != "" || req.FingerprintKey != "")
	for _, path := range []string{req.StoredFilePath, req.FingerprintKey} {
		if path == "" {
			continue
		}
		for _, root := range s.roots {
			if source, ok := s.sourceRef(ctx, root, path, true); ok {
				src := source.Opaque.(antigravitySource)
				if req.RawSessionID != "" && src.ID != req.RawSessionID {
					continue
				}
				if req.RequireFreshSource && !IsRegularFile(src.Path) {
					continue
				}
				return source, true, nil
			}
		}
	}
	if freshStoredSource {
		return SourceRef{}, false, nil
	}
	if req.RawSessionID == "" {
		return SourceRef{}, false, nil
	}
	for _, root := range s.roots {
		path := s.findSourceFile(root, req.RawSessionID)
		if path == "" {
			continue
		}
		if source, ok := s.sourceRef(ctx, root, path, false); ok {
			return source, true, nil
		}
	}
	return SourceRef{}, false, nil
}

func (s antigravitySourceSet) Fingerprint(
	ctx context.Context,
	source SourceRef,
) (SourceFingerprint, error) {
	if err := ctx.Err(); err != nil {
		return SourceFingerprint{}, err
	}
	src, ok := s.sourceFromRef(ctx, source)
	if !ok {
		return SourceFingerprint{}, errors.New("antigravity source path unavailable")
	}
	key := firstNonEmptyJSONLString(source.FingerprintKey, source.Key, src.Path)
	info, err := AntigravityFileInfo(src.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return SourceFingerprint{Key: key}, nil
		}
		return SourceFingerprint{}, err
	}
	companions := antigravityIDECompanionPaths(src.Path)
	// A resolved subagent descriptor is hashed as a companion of the
	// child it claims so a descriptor write/delete alone re-syncs the
	// child's parent link. The parent's companions never include it:
	// parent parse does not read .system_generated.
	if d, ok := s.subagentDescriptor(src.Root, src.ID); ok {
		companions = append(companions, d.path)
	}
	hash, err := antigravityCompositeHash(src.Path, companions...)
	if err != nil {
		return SourceFingerprint{}, err
	}
	return SourceFingerprint{
		Key:     key,
		Size:    info.Size(),
		MTimeNS: info.ModTime().UnixNano(),
		Hash:    hash,
	}, nil
}

func (s antigravitySourceSet) sourceFromRef(
	ctx context.Context, source SourceRef,
) (antigravitySource, bool) {
	switch src := source.Opaque.(type) {
	case antigravitySource:
		return src, src.Path != ""
	case *antigravitySource:
		if src != nil && src.Path != "" {
			return *src, true
		}
	}
	for _, candidate := range []string{source.DisplayPath, source.FingerprintKey, source.Key} {
		for _, root := range s.roots {
			if ref, ok := s.sourceRef(ctx, root, candidate, true); ok {
				src := ref.Opaque.(antigravitySource)
				return src, true
			}
		}
	}
	return antigravitySource{}, false
}

func (s antigravitySourceSet) sourceForChangedPath(
	ctx context.Context, root, path string,
) (SourceRef, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if dbPath, _, ok := antigravityConversationDBForPath(root, path); ok {
		return s.sourceRef(ctx, root, dbPath, true)
	}
	if childID, ok := antigravitySubagentDescriptorID(root, path); ok {
		// A descriptor create/write/remove can change the child's
		// parent link, so refresh the cached entry before the
		// returned source ref is built.
		s.subagents.refresh(root, childID)
		dbPath := filepath.Join(root, "conversations", childID+".db")
		if IsRegularFile(dbPath) {
			return s.newSourceRef(ctx, root, dbPath, childID), true
		}
		return SourceRef{}, false
	}
	if id, ok := antigravityIDETrajectoryID(root, path); ok {
		dbPath := filepath.Join(root, "conversations", id+".db")
		if IsRegularFile(dbPath) {
			return s.newSourceRef(ctx, root, dbPath, id), true
		}
	}
	if id, ok := antigravityAnnotationID(root, path); ok {
		dbPath := filepath.Join(root, "conversations", id+".db")
		if IsRegularFile(dbPath) {
			return s.newSourceRef(ctx, root, dbPath, id), true
		}
	}
	if transcriptRoot, _, ok := antigravityBrainTranscriptConversation(path); ok &&
		samePath(transcriptRoot, root) {
		return s.sourceRef(ctx, root, path, true)
	}
	if id, ok := antigravityBrainID(root, path); ok {
		dbPath := filepath.Join(root, "conversations", id+".db")
		if IsRegularFile(dbPath) {
			return s.newSourceRef(ctx, root, dbPath, id), true
		}
	}
	return SourceRef{}, false
}

func (s antigravitySourceSet) sourceRef(
	ctx context.Context,
	root, path string,
	allowMissing bool,
) (SourceRef, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if transcriptRoot, id, ok := antigravityBrainTranscriptConversation(path); ok &&
		samePath(transcriptRoot, root) {
		// A conversation that also has a database is that database's session;
		// the transcript joins it as a companion instead of standing alone.
		dbPath := filepath.Join(root, "conversations", id+".db")
		if IsRegularFile(dbPath) {
			return s.newSourceRef(ctx, root, dbPath, id), true
		}
		if !allowMissing && !IsRegularFile(path) {
			return SourceRef{}, false
		}
		return s.newSourceRef(ctx, root, path, id), true
	}
	dbPath, id, ok := antigravityConversationDBForPath(root, path)
	if !ok || dbPath != path {
		return SourceRef{}, false
	}
	// A removed database hands the same conversation back to its transcript.
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		transcript := antigravityBrainTranscriptPath(root, id)
		if IsRegularFile(transcript) {
			return s.newSourceRef(ctx, root, transcript, id), true
		}
	}
	if !allowMissing && !IsRegularFile(path) {
		return SourceRef{}, false
	}
	return s.newSourceRef(ctx, root, path, id), true
}

func (s antigravitySourceSet) newSourceRef(
	ctx context.Context, root, path, id string,
) SourceRef {
	src := antigravitySource{Root: root, Path: path, ID: id}
	if d, ok := s.subagents.lookup(root, id); ok {
		src.SubagentParentID = d.parentID
		src.SubagentRole = d.role
	}
	var projectHint string
	var resolution SourceCwdResolution
	if sum, ok := s.summaries.lookup(ctx, root, id); ok {
		projectHint = sum.project
		if sum.cwd != "" {
			resolution = SourceCwdResolution{State: SourceCwdResolved, Path: sum.cwd}
		}
	}
	return SourceRef{
		Provider:       AgentAntigravity,
		Key:            path,
		DisplayPath:    path,
		FingerprintKey: path,
		ProjectHint:    projectHint,
		CwdResolution:  resolution,
		Opaque:         src,
	}
}

func antigravityConversationDBForPath(root, path string) (string, string, bool) {
	rel, ok := relUnder(filepath.Clean(root), filepath.Clean(path))
	if !ok {
		return "", "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || parts[0] != "conversations" {
		return "", "", false
	}
	// A bare "-shm" event never resolves to the session: every parse's
	// read-only open rewrites that index, so honoring it would make each
	// parse schedule the next one. Committed writes land in the main file
	// or the -wal.
	name := strings.TrimSuffix(parts[1], "-wal")
	if !strings.HasSuffix(name, ".db") {
		return "", "", false
	}
	id := strings.TrimSuffix(name, ".db")
	if !IsValidSessionID(id) {
		return "", "", false
	}
	return filepath.Join(root, "conversations", id+".db"), id, true
}

// antigravityIDETrajectoryID reports whether path is a
// conversations/<id>.trajectory.json agy-reader sidecar under root and,
// if so, returns the session id so a sidecar write routes back to its
// .db source for re-sync.
func antigravityIDETrajectoryID(root, path string) (string, bool) {
	rel, ok := relUnder(filepath.Clean(root), filepath.Clean(path))
	if !ok {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || parts[0] != "conversations" ||
		!strings.HasSuffix(parts[1], ".trajectory.json") {
		return "", false
	}
	id := strings.TrimSuffix(parts[1], ".trajectory.json")
	return id, IsValidSessionID(id)
}

func antigravityAnnotationID(root, path string) (string, bool) {
	rel, ok := relUnder(filepath.Clean(root), filepath.Clean(path))
	if !ok {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || parts[0] != "annotations" ||
		!strings.HasSuffix(parts[1], ".pbtxt") {
		return "", false
	}
	id := strings.TrimSuffix(parts[1], ".pbtxt")
	return id, IsValidSessionID(id)
}

func antigravityBrainID(root, path string) (string, bool) {
	rel, ok := relUnder(filepath.Clean(root), filepath.Clean(path))
	if !ok {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 2 || parts[0] != "brain" {
		return "", false
	}
	return parts[1], IsValidSessionID(parts[1])
}

func antigravityWatchRootMatches(root, watchRoot string) bool {
	watchRoot = filepath.Clean(watchRoot)
	for _, subdir := range []string{"annotations", "brain", "conversations"} {
		if samePath(watchRoot, filepath.Join(root, subdir)) {
			return true
		}
	}
	return samePath(watchRoot, filepath.Clean(root))
}

func antigravityProviderCapabilities() Capabilities {
	source := jsonlFileProviderSourceCapabilities()
	source.StreamingDiscovery = CapabilitySupported
	source.ForceReplaceOnParse = CapabilitySupported
	source.ChangedPathRelevance = CapabilitySupported
	source.MultiFileStatHash = CapabilitySupported
	return Capabilities{
		Source: source,
		Sync: ProviderSyncSemantics{
			// The stored size/mtime of the composite stat cannot distinguish
			// a same-size, same-mtime rewrite (or a rename-swap that lands in
			// the same clock tick), so the fingerprint's content SHA must
			// gate every stat-matching skip. The per-component stat digest
			// (MultiFileStatHash) keeps warm passes stat-only; the hash gate
			// is what makes a broken digest or an unverifiable identity
			// field force a real re-verification instead of a stale skip.
			FingerprintHashRequiredForFreshness: true,
		},
		Content: ContentCapabilities{
			FirstMessage:         CapabilitySupported,
			SessionName:          CapabilitySupported,
			Cwd:                  CapabilitySupported,
			Thinking:             CapabilitySupported,
			ToolCalls:            CapabilitySupported,
			ToolResults:          CapabilitySupported,
			PerMessageTokenUsage: CapabilitySupported,
			AggregateUsageEvents: CapabilitySupported,
			Model:                CapabilitySupported,
		},
	}
}
