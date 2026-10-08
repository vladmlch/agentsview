package parser

import (
	"encoding/json/v2"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Antigravity IDE records spawned subagents in
// brain/<parentUUID>/.system_generated/subagents/<childUUID>.json. The
// descriptor is the authoritative parent->child edge; the filename equals
// the child's conversationId. One invoke_subagent call may spawn a batch
// of children sharing the same spawnStepIndex (the result step that
// echoes each spawned conversation id back to the planner).

// antigravitySubagentsDir is the descriptor directory name inside a
// brain conversation's .system_generated tree.
const antigravitySubagentsDir = "subagents"

type antigravitySubagentDescriptor struct {
	childID        string
	parentID       string
	role           string
	typeName       string
	spawnStepIndex int
	path           string
}

// sessionName prefers the human-authored subagent role and falls back
// to the descriptor's type name so a role-less child still gets a
// descriptive session name.
func (d antigravitySubagentDescriptor) sessionName() string {
	return firstNonEmptyJSONLString(d.role, d.typeName)
}

type antigravitySubagentDescriptorJSON struct {
	ConversationID string `json:"conversationId"`
	SpawnStepIndex int    `json:"spawnStepIndex"`
	Descriptor     struct {
		TypeName string `json:"typeName"`
		Role     string `json:"role"`
	} `json:"subagentDescriptor"`
}

// antigravitySubagentIndex caches child-id -> descriptor resolutions for
// one provider instance so discovery, changed-path routing, fingerprint,
// and parse share a single scan of the brain descriptor dirs. It is a
// pointer field on the value-typed antigravitySourceSet, so every copied
// method receiver still sees the same cache.
type antigravitySubagentIndex struct {
	mu      sync.RWMutex
	entries map[string]antigravitySubagentDescriptor
}

func newAntigravitySubagentIndex() *antigravitySubagentIndex {
	return &antigravitySubagentIndex{
		entries: make(map[string]antigravitySubagentDescriptor),
	}
}

func antigravitySubagentKey(root, childID string) string {
	return filepath.Clean(root) + "\x00" + childID
}

// lookup returns the cached descriptor for childID under root. Only
// descriptor files that passed validation are cached, so a hit is
// already link-safe.
func (x *antigravitySubagentIndex) lookup(
	root, childID string,
) (antigravitySubagentDescriptor, bool) {
	if x == nil {
		return antigravitySubagentDescriptor{}, false
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	d, ok := x.entries[antigravitySubagentKey(root, childID)]
	return d, ok
}

// scan rebuilds root's descriptor entries from one glob pass over
// brain/*/.system_generated/subagents/*.json. Stale entries for removed
// or rewritten descriptors are dropped with the rebuild.
func (x *antigravitySubagentIndex) scan(root string) {
	if x == nil {
		return
	}
	paths, err := filepath.Glob(filepath.Join(
		root, "brain", "*", antigravityBrainGeneratedDir,
		antigravitySubagentsDir, "*.json",
	))
	if err != nil {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	prefix := antigravitySubagentKey(root, "")
	for key := range x.entries {
		if strings.HasPrefix(key, prefix) {
			delete(x.entries, key)
		}
	}
	x.upsertPathsLocked(root, paths)
}

// refresh re-resolves one child id after a descriptor path event. The
// rescan is a single glob over the child's descriptor files across all
// parent dirs, so a deleted or malformed descriptor drops the link and a
// competing parent's surviving descriptor takes over.
func (x *antigravitySubagentIndex) refresh(root, childID string) {
	if x == nil {
		return
	}
	paths, err := filepath.Glob(filepath.Join(
		root, "brain", "*", antigravityBrainGeneratedDir,
		antigravitySubagentsDir, childID+".json",
	))
	if err != nil {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.entries, antigravitySubagentKey(root, childID))
	x.upsertPathsLocked(root, paths)
}

// upsertPathsLocked inserts each valid descriptor. When two parents
// claim the same child the first (glob-sorted) entry wins and the
// conflict is logged without ids or roles.
func (x *antigravitySubagentIndex) upsertPathsLocked(
	root string, paths []string,
) {
	for _, path := range paths {
		d, ok := readAntigravitySubagentDescriptor(path)
		if !ok {
			continue
		}
		key := antigravitySubagentKey(root, d.childID)
		if prev, exists := x.entries[key]; exists {
			if prev.parentID != d.parentID {
				log.Printf(
					"antigravity: conflicting subagent descriptors; keeping first",
				)
			}
			continue
		}
		x.entries[key] = d
	}
}

// readAntigravitySubagentDescriptor parses and validates one descriptor
// file. Skipped: malformed JSON, an empty or non-UUID conversationId, a
// conversationId that differs from the filename, a non-UUID parent dir,
// and a self link. The link is trusted regardless of the descriptor's
// state value.
func readAntigravitySubagentDescriptor(
	path string,
) (antigravitySubagentDescriptor, bool) {
	base := filepath.Base(path)
	childID := canonicalAgyCascadeID(strings.TrimSuffix(base, ".json"))
	// path = <root>/brain/<parent>/.system_generated/subagents/<child>.json
	parentID := canonicalAgyCascadeID(
		filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(path)))),
	)
	if childID == "" || parentID == "" || childID == parentID {
		return antigravitySubagentDescriptor{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return antigravitySubagentDescriptor{}, false
	}
	var doc antigravitySubagentDescriptorJSON
	if err := json.Unmarshal(data, &doc); err != nil {
		return antigravitySubagentDescriptor{}, false
	}
	if canonicalAgyCascadeID(doc.ConversationID) != childID {
		return antigravitySubagentDescriptor{}, false
	}
	return antigravitySubagentDescriptor{
		childID:        childID,
		parentID:       parentID,
		role:           doc.Descriptor.Role,
		typeName:       doc.Descriptor.TypeName,
		spawnStepIndex: doc.SpawnStepIndex,
		path:           path,
	}, true
}

// findAntigravitySubagentDescriptor resolves a child id's descriptor by
// globbing its descriptor files across parents. It is the bounded
// fallback for refs that were built without map data (stored-path
// lookups, ad-hoc sources); per-event work stays at one glob.
func findAntigravitySubagentDescriptor(
	root, childID string,
) (antigravitySubagentDescriptor, bool) {
	if !IsValidSessionID(childID) {
		return antigravitySubagentDescriptor{}, false
	}
	paths, err := filepath.Glob(filepath.Join(
		root, "brain", "*", antigravityBrainGeneratedDir,
		antigravitySubagentsDir, childID+".json",
	))
	if err != nil {
		return antigravitySubagentDescriptor{}, false
	}
	for _, path := range paths {
		if d, ok := readAntigravitySubagentDescriptor(path); ok {
			return d, true
		}
	}
	return antigravitySubagentDescriptor{}, false
}

// antigravitySubagentDescriptorID reports whether path is a
// brain/<parent>/.system_generated/subagents/<child>.json descriptor
// under root and returns the child session id.
func antigravitySubagentDescriptorID(root, path string) (string, bool) {
	rel, ok := relUnder(filepath.Clean(root), filepath.Clean(path))
	if !ok {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 5 || parts[0] != "brain" ||
		parts[2] != antigravityBrainGeneratedDir ||
		parts[3] != antigravitySubagentsDir ||
		!strings.HasSuffix(parts[4], ".json") {
		return "", false
	}
	id := strings.TrimSuffix(parts[4], ".json")
	return id, IsValidSessionID(id)
}

// subagentDescriptor resolves a session's subagent descriptor from the
// cached index, falling back to one bounded glob for refs that carry no
// map data.
func (s antigravitySourceSet) subagentDescriptor(
	root, id string,
) (antigravitySubagentDescriptor, bool) {
	if d, ok := s.subagents.lookup(root, id); ok {
		return d, true
	}
	return findAntigravitySubagentDescriptor(root, id)
}

// antigravitySubagentChainLimit bounds the descriptor parent-chain walk
// in subagentChainCycles, matching the codex continuation-chain cap.
const antigravitySubagentChainLimit = 20

// subagentChainCycles reports whether the descriptor parent chain
// starting at parentID leads back to id. Descriptor validation rejects
// only a direct self link, so a cyclic pair (A claimed by B while B is
// claimed by A) or a longer loop would otherwise be emitted as edges
// and make every member somebody's child -- dropping all of them from
// the sidebar roots. The walk stops at the first ancestor with no
// descriptor claim and is bounded at antigravitySubagentChainLimit.
func (s antigravitySourceSet) subagentChainCycles(
	root, id, parentID string,
) bool {
	if id == "" || parentID == "" {
		return false
	}
	visited := map[string]struct{}{id: {}}
	cur := parentID
	for i := 0; i < antigravitySubagentChainLimit; i++ {
		if _, seen := visited[cur]; seen {
			return true
		}
		visited[cur] = struct{}{}
		d, ok := s.subagentDescriptor(root, cur)
		if !ok {
			return false
		}
		cur = d.parentID
	}
	return false
}

// antigravitySubagentResultWindow bounds how far past an invoke_subagent
// planner step the spawned-conversation scan reaches. The result step
// follows the call within a few rows; a wide window risks attributing a
// later batch's children to the wrong call.
const antigravitySubagentResultWindow = 3

// antigravitySubagentSpawnMarker appears only in spawn-result steps.
// It gates conversationId extraction: manage_subagents list results
// quote genuine child conversation ids and define_subagent results can
// quote invoke_subagent payloads, so an unmarked id is never a spawn
// edge.
const antigravitySubagentSpawnMarker = "Created the following subagents"

// antigravityConversationIDRE extracts a spawned conversation id from a
// marked result step. The tool emits "conversationId" (camelCase);
// older builds wrote "conversation_id" (snake_case) and both spellings
// appear in real payloads, so both are matched.
var antigravityConversationIDRE = regexp.MustCompile(
	`"conversation(?:Id|_id)"\s*:\s*"` +
		`([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-` +
		`[0-9a-fA-F]{4}-[0-9a-fA-F]{12})"`,
)

// linkAntigravitySubagentSpawnEdges attaches a spawned session id to an
// invoke_subagent tool call. Only steps carrying the
// "Created the following subagents" marker are spawn results; unmarked
// results -- including manage_subagents list output that legitimately
// names running children -- are never scanned.
//
// An edge is emitted only when the call<->result pairing is
// unambiguous: exactly one invoke_subagent call in the planner step and
// exactly one marked result step inside the window. Every conversation
// id that step lists is then a child of that call, so storing the first
// id is a true edge whichever sibling the batch carried. Anything else
// is refused: several invoke calls in one step or several marked steps
// in the window cannot be paired positionally with confidence -- result
// order is not guaranteed to match call order, and a marked step can be
// a result belonging to a different call. A wrong edge is worse than
// none because the archive linker (subagentSpawnerExpr) repoints the
// child's parent_session_id from it, overriding the authoritative
// descriptor link.
func linkAntigravitySubagentSpawnEdges(
	steps []antigravityLoadedStep, sessionIDPrefix string,
) {
	for i := range steps {
		var spawnCalls []int
		if steps[i].decoded {
			for k, call := range steps[i].msg.ToolCalls {
				if call.ToolName == "invoke_subagent" {
					spawnCalls = append(spawnCalls, k)
				}
			}
		}
		if len(spawnCalls) == 0 {
			continue
		}
		var markedIDs [][]string
		for j := i + 1; j < len(steps) &&
			j-i <= antigravitySubagentResultWindow; j++ {
			next := steps[j]
			if next.kind == antigravityStepKindPlannerResponse {
				// Another planner step means the result step was
				// missed; ids beyond it belong to a different call.
				break
			}
			strs := agProtoCollectStrings(next.fields, 1)
			marked := false
			for _, s := range strs {
				if strings.Contains(
					s, antigravitySubagentSpawnMarker,
				) {
					marked = true
					break
				}
			}
			if !marked {
				continue
			}
			var ids []string
			for _, s := range strs {
				for _, m := range antigravityConversationIDRE.
					FindAllStringSubmatch(s, -1) {
					if id := canonicalAgyCascadeID(m[1]); id != "" {
						ids = append(ids, id)
					}
				}
			}
			markedIDs = append(markedIDs, ids)
		}
		if len(spawnCalls) != 1 || len(markedIDs) != 1 ||
			len(markedIDs[0]) == 0 {
			// Ambiguous pairing -- emit nothing rather than a guess.
			continue
		}
		steps[i].msg.ToolCalls[spawnCalls[0]].SubagentSessionID =
			sessionIDPrefix + markedIDs[0][0]
	}
}
