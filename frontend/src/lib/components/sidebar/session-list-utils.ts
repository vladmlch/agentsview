import type { SessionGroup, SessionGroupInput } from "../../stores/sessions.svelte.js";
import { buildSessionGroups } from "../../stores/sessions.svelte.js";
import { sessionAncestryMatches } from "../../utils/session-ancestry.js";

export const ITEM_HEIGHT = 42;
const CHILD_ITEM_HEIGHT = 34;
const TEAM_HEADER_HEIGHT = 28;
export const HEADER_HEIGHT = 28;
export const OVERSCAN = 10;
export const STORAGE_KEY = "agentsview-group-by-agent";
export const STORAGE_KEY_GROUP = "agentsview-group-mode";

export type GroupMode = "none" | "agent" | "project";

export interface GroupSection {
  label: string;
  groups: SessionGroup[];
  /** Number of unique sessions currently loaded for the section — the
   *  union of the flat list slice and the per-group cache. `totalCount`
   *  is a server-side session count, so remaining-row math must compare
   *  against this, not against `groups.length` (continuation chains
   *  collapse several sessions into one group). */
  sessionCount?: number;
  totalCount?: number;
  hasMore?: boolean;
  /** The section's first-page load failed; display can offer a retry. */
  error?: boolean;
}

export interface GroupMetadataItem {
  key: string;
  totalCount: number;
}

/**
 * Per-label loading state for lazily fetched group sections.
 * `sessions` are the raw cached rows for the label; they are merged with
 * the flat list inside `buildGroupSections` so deep-linked or freshly
 * appended sessions are never dropped when a cache entry exists.
 */
export interface PerGroupSectionState {
  sessions: SessionGroupInput[];
  totalCount?: number;
  hasMore?: boolean;
  error?: boolean;
}

/** @deprecated Use GroupSection */
export type AgentSection = GroupSection;

export interface DisplayItem {
  id: string;
  type:
    | "header"
    | "session"
    | "team-group"
    | "subagent-group"
    | "group-loading"
    | "group-load-more"
    | "group-error";
  label: string;
  count: number;
  group?: SessionGroup;
  /** Session groups represented by a collapsible section header. */
  sectionGroups?: SessionGroup[];
  /** Sessions represented by a collapsible child-group header. */
  memberSessionIds?: string[];
  /** For child items within an expanded continuation chain. */
  session?: SessionGroupInput;
  /** True when this is a child session inside an expanded group. */
  isChild?: boolean;
  /** Nesting depth: 0 = root, 1 = child/team-group, 2 = teammate. */
  depth?: number;
  /** True when this is the last sibling at its depth level. */
  isLastChild?: boolean;
  /** True when this depth-1 item has a following depth-1 sibling
   *  (used to draw the vertical connector line through this row). */
  hasNextSiblingAtDepth1?: boolean;
  height: number;
  top: number;
}

/**
 * Read persisted group mode from localStorage, migrating the
 * legacy boolean key if needed.
 */
export function getInitialGroupMode(): GroupMode {
  if (typeof localStorage === "undefined") return "none";
  const stored = localStorage.getItem(STORAGE_KEY_GROUP);
  if (stored === "agent" || stored === "project") return stored;
  // Legacy migration
  if (localStorage.getItem(STORAGE_KEY) === "true") return "agent";
  return "none";
}

export { selectPrimaryId } from "../../stores/sessions.svelte.js";

/**
 * Union of the sessions a label has in the per-group cache and in the
 * flat loaded list, deduped by id. The flat copy wins ties (it is kept
 * fresh by rename/assignment merges), except that a skinny index row
 * never displaces a hydrated one. Merging matters because a cache entry
 * used to mask the flat list entirely, hiding deep-linked sessions that
 * only ever landed in `sessions.sessions`.
 */
function mergeSectionSessions(
  cached: SessionGroupInput[] | undefined,
  flat: SessionGroup[] | undefined,
): SessionGroupInput[] {
  const byId = new Map<string, SessionGroupInput>();
  for (const s of cached ?? []) {
    byId.set(s.id, s);
  }
  for (const g of flat ?? []) {
    for (const s of g.sessions) {
      const prev = byId.get(s.id);
      if (prev && !prev.is_index_only && s.is_index_only) continue;
      byId.set(s.id, s);
    }
  }
  return [...byId.values()];
}

/**
 * Build grouped sections from flat session groups.
 * Groups by agent name or project depending on mode.
 * Returns empty array when mode is "none".
 *
 * `metadata` (agents/projects endpoints) only knows the
 * include_one_shot/include_automated flags — callers must omit it while
 * any other filter is active so sections and counters reflect the
 * filtered list instead of unfiltered totals.
 */
export function buildGroupSections(
  groups: SessionGroup[],
  mode: GroupMode,
  metadata?: GroupMetadataItem[],
  perGroupSessions?: Map<string, PerGroupSectionState>,
): GroupSection[] {
  if (mode === "none") return [];
  const map = new Map<string, SessionGroup[]>();
  for (const g of groups) {
    const primary = g.sessions.find((s) => s.id === g.primarySessionId) ?? g.sessions[0];
    if (!primary) continue;
    const key = mode === "agent" ? primary.agent : primary.project;
    let list = map.get(key);
    if (!list) {
      list = [];
      map.set(key, list);
    }
    list.push(g);
  }

  // Resolve the merged content of one label. When the per-group cache
  // has an entry, its sessions are unioned with the flat slice and
  // re-grouped so chains spanning the two sources stay intact.
  const sectionFor = (label: string): GroupSection => {
    const flat = map.get(label);
    const perGroup = perGroupSessions?.get(label);
    const merged = mergeSectionSessions(perGroup?.sessions, flat);
    const sectionGroups = perGroup ? buildSessionGroups(merged) : (flat ?? []);
    return {
      label,
      groups: sectionGroups,
      sessionCount: merged.length,
      error: perGroup?.error,
    };
  };

  const byTotalDesc = (a: GroupSection, b: GroupSection) => {
    const diff =
      (b.totalCount ?? b.sessionCount ?? b.groups.length) -
      (a.totalCount ?? a.sessionCount ?? a.groups.length);
    if (diff !== 0) return diff;
    return a.label.localeCompare(b.label);
  };

  if (!metadata || metadata.length === 0) {
    // Sections come from the loaded (filtered) list plus any cached
    // per-group pages — the BASE behavior for filtered views.
    const labels = new Set<string>([
      ...map.keys(),
      ...(perGroupSessions?.keys() ?? []),
    ]);
    const sections: GroupSection[] = [];
    for (const label of labels) {
      const section = sectionFor(label);
      const perGroup = perGroupSessions?.get(label);
      if (section.groups.length === 0 && !section.error) continue;
      sections.push({
        ...section,
        totalCount: perGroup?.totalCount ?? section.sessionCount,
        hasMore: perGroup?.hasMore ?? false,
      });
    }
    return sections.sort(byTotalDesc);
  }

  const seen = new Set<string>();
  const sections: GroupSection[] = [];

  for (const meta of metadata) {
    const label = meta.key;
    seen.add(label);
    const section = sectionFor(label);
    const perGroup = perGroupSessions?.get(label);
    const totalCount = perGroup?.totalCount ?? meta.totalCount;
    sections.push({
      ...section,
      totalCount,
      hasMore: perGroup?.hasMore ?? totalCount > (section.sessionCount ?? 0),
    });
  }

  for (const label of map.keys()) {
    if (!seen.has(label)) {
      const section = sectionFor(label);
      const perGroup = perGroupSessions?.get(label);
      sections.push({
        ...section,
        totalCount: perGroup?.totalCount ?? section.sessionCount,
        hasMore: perGroup?.hasMore ?? false,
      });
      seen.add(label);
    }
  }

  // A cache entry whose label is absent from both the metadata and the
  // flat list still holds real loaded rows — keep it visible.
  for (const label of perGroupSessions?.keys() ?? []) {
    if (seen.has(label)) continue;
    const section = sectionFor(label);
    if (section.groups.length === 0 && !section.error) continue;
    const perGroup = perGroupSessions?.get(label);
    sections.push({
      ...section,
      totalCount: perGroup?.totalCount ?? section.sessionCount,
      hasMore: perGroup?.hasMore ?? false,
    });
  }

  return sections.sort(byTotalDesc);
}

/** Check if a session is a teammate (received a <teammate-message>). */
function isTeammateByMessage(s: SessionGroupInput): boolean {
  return s.is_teammate ?? s.first_message?.includes("<teammate-message") ?? false;
}

/**
 * Check if a session is a teammate, inheriting status from the
 * parent.  Continuation sessions of a teammate don't carry the
 * `<teammate-message>` tag themselves, but they belong to the
 * same teammate chain.
 */
function isTeammate(s: SessionGroupInput, allSessions: SessionGroupInput[]): boolean {
  return sessionAncestryMatches(s, allSessions, isTeammateByMessage);
}

/**
 * Check if a session is a subagent (has relationship_type === "subagent").
 * Continuation/fork sessions are NOT subagents.
 */
function isSubagent(s: SessionGroupInput): boolean {
  return s.relationship_type === "subagent";
}

/**
 * Check if a session is a subagent descendant — either it has
 * relationship_type === "subagent" itself, or one of its ancestors
 * in the group does.  This ensures that continuations/forks of a
 * subagent stay under the "Subagents" group header instead of
 * being miscategorised as plain continuations.
 */
export function isSubagentDescendant(
  s: SessionGroupInput,
  groupSessions: SessionGroupInput[],
): boolean {
  return sessionAncestryMatches(s, groupSessions, isSubagent);
}

/**
 * Emit display items for a single SessionGroup, expanding
 * child sessions when the group key is in expandedGroups.
 *
 * When a group contains teammate sessions, they are placed
 * under a synthetic "Team (N)" expandable node at depth 1,
 * and the teammates themselves render at depth 2. Regular
 * subagents remain at depth 1. This gives the 3-level tree:
 *   Session (depth 0) > Subagent (depth 1)
 *   Session (depth 0) > Team (depth 1) > Teammate (depth 2)
 */
function emitGroupItems(
  g: SessionGroup,
  label: string,
  expandedGroups: Set<string>,
  items: DisplayItem[],
  y: { value: number },
): void {
  const hasChildren = g.sessions.length > 1;
  const isExpanded = hasChildren && expandedGroups.has(g.key);

  // Primary session (depth 0)
  items.push({
    id: label ? `session:${label}:${g.primarySessionId}` : `session:${g.primarySessionId}`,
    type: "session",
    label,
    count: 0,
    group: g,
    depth: 0,
    height: ITEM_HEIGHT,
    top: y.value,
  });
  y.value += ITEM_HEIGHT;

  if (!isExpanded) return;

  // Separate children into subagents, teammates, and continuations.
  // Use allSessions (unfiltered) for ancestry classification so that
  // filtered-out ancestors don't break the parent-chain walk.
  const children = g.sessions.filter((s) => s.id !== g.primarySessionId);
  const ancestryPool = g.allSessions ?? g.sessions;
  const subagents: SessionGroupInput[] = [];
  const teammates: SessionGroupInput[] = [];
  const continuations: SessionGroupInput[] = [];
  for (const s of children) {
    if (isSubagentDescendant(s, ancestryPool)) {
      subagents.push(s);
    } else if (isTeammate(s, ancestryPool)) {
      teammates.push(s);
    } else {
      continuations.push(s);
    }
  }

  // Sort each child group most-recent-first so the sidebar
  // matches the main session list ordering convention.
  const byRecencyDesc = (a: SessionGroupInput, b: SessionGroupInput) => {
    const ka = a.ended_at ?? a.started_at ?? a.created_at;
    const kb = b.ended_at ?? b.started_at ?? b.created_at;
    return ka > kb ? -1 : ka < kb ? 1 : 0;
  };
  continuations.sort(byRecencyDesc);
  subagents.sort(byRecencyDesc);
  teammates.sort(byRecencyDesc);

  // Continuations render inline at depth 1 (no sub-group header).
  for (let i = 0; i < continuations.length; i++) {
    const s = continuations[i]!;
    // Determine if this is the last depth-1 sibling (accounting
    // for subsequent sub-group headers).
    const hasFollowingGroup = subagents.length > 0 || teammates.length > 0;
    const isLast = i === continuations.length - 1 && !hasFollowingGroup;
    items.push({
      id: `child:${g.key}:${s.id}`,
      type: "session",
      label,
      count: 0,
      group: g,
      session: s,
      isChild: true,
      depth: 1,
      isLastChild: isLast,
      height: CHILD_ITEM_HEIGHT,
      top: y.value,
    });
    y.value += CHILD_ITEM_HEIGHT;
  }

  // Count depth-1 group headers (subagents + team).
  const hasSubagentGroup = subagents.length > 0;
  const hasTeamGroup = teammates.length > 0;
  const depth1Count = (hasSubagentGroup ? 1 : 0) + (hasTeamGroup ? 1 : 0);
  let depth1Idx = 0;

  // Emit "Subagents (N)" group header + children at depth 2.
  if (hasSubagentGroup) {
    const subKey = `subagent:${g.key}`;
    const subExpanded = expandedGroups.has(subKey);

    items.push({
      id: `subagent-group:${g.key}`,
      type: "subagent-group",
      label: "Subagents",
      count: subagents.length,
      group: g,
      memberSessionIds: subagents.map((session) => session.id),
      depth: 1,
      isLastChild: depth1Idx === depth1Count - 1,
      height: TEAM_HEADER_HEIGHT,
      top: y.value,
    });
    y.value += TEAM_HEADER_HEIGHT;
    depth1Idx++;

    if (subExpanded) {
      for (let i = 0; i < subagents.length; i++) {
        const s = subagents[i]!;
        items.push({
          id: `child:${g.key}:${s.id}`,
          type: "session",
          label,
          count: 0,
          group: g,
          session: s,
          isChild: true,
          depth: 2,
          isLastChild: i === subagents.length - 1,
          height: CHILD_ITEM_HEIGHT,
          top: y.value,
        });
        y.value += CHILD_ITEM_HEIGHT;
      }
    }
  }

  // Emit "Team (N)" group header + children at depth 2.
  if (hasTeamGroup) {
    const teamKey = `team:${g.key}`;
    const teamExpanded = expandedGroups.has(teamKey);

    items.push({
      id: `team-group:${g.key}`,
      type: "team-group",
      label: "Team",
      count: teammates.length,
      group: g,
      memberSessionIds: teammates.map((session) => session.id),
      depth: 1,
      isLastChild: depth1Idx === depth1Count - 1,
      height: TEAM_HEADER_HEIGHT,
      top: y.value,
    });
    y.value += TEAM_HEADER_HEIGHT;

    if (teamExpanded) {
      for (let i = 0; i < teammates.length; i++) {
        const s = teammates[i]!;
        items.push({
          id: `child:${g.key}:${s.id}`,
          type: "session",
          label,
          count: 0,
          group: g,
          session: s,
          isChild: true,
          depth: 2,
          isLastChild: i === teammates.length - 1,
          height: CHILD_ITEM_HEIGHT,
          top: y.value,
        });
        y.value += CHILD_ITEM_HEIGHT;
      }
    }
  }
}

/**
 * Build a flat list of DisplayItems for virtual scrolling.
 * When mode is "none", produces a simple flat list.
 * Otherwise, interleaves header rows and session rows,
 * respecting collapsed groups. Continuation chains expand
 * inline when their group key is in expandedGroups.
 */
export function buildDisplayItems(
  groups: SessionGroup[],
  sections: GroupSection[],
  mode: GroupMode,
  collapsed: Set<string>,
  expandedGroups: Set<string>,
  groupLoading?: Set<string>,
): DisplayItem[] {
  const y = { value: 0 };

  if (mode === "none") {
    const items: DisplayItem[] = [];
    for (const g of groups) {
      emitGroupItems(g, "", expandedGroups, items, y);
    }
    return items;
  }

  const items: DisplayItem[] = [];
  for (const section of sections) {
    const count = section.totalCount ?? section.groups.length;
    items.push({
      id: `header:${section.label}`,
      type: "header",
      label: section.label,
      count,
      sectionGroups: section.groups,
      height: HEADER_HEIGHT,
      top: y.value,
    });
    y.value += HEADER_HEIGHT;

    if (!collapsed.has(section.label)) {
      const isLoading = groupLoading?.has(section.label);
      if (section.groups.length === 0 && isLoading) {
        items.push({
          id: `loading:${section.label}`,
          type: "group-loading",
          label: section.label,
          count: 0,
          height: ITEM_HEIGHT,
          top: y.value,
        });
        y.value += ITEM_HEIGHT;
      } else {
        for (const g of section.groups) {
          emitGroupItems(g, section.label, expandedGroups, items, y);
        }
        if (section.groups.length === 0 && section.error) {
          items.push({
            id: `error:${section.label}`,
            type: "group-error",
            label: section.label,
            count: 0,
            height: ITEM_HEIGHT,
            top: y.value,
          });
          y.value += ITEM_HEIGHT;
        } else if (section.hasMore) {
          items.push({
            id: `more:${section.label}`,
            type: "group-load-more",
            label: section.label,
            // totalCount counts sessions while groups.length counts
            // continuation chains — the remainder must be computed in
            // sessions or multi-session chains inflate it.
            count: Math.max(
              0,
              (section.totalCount ?? 0) - (section.sessionCount ?? section.groups.length),
            ),
            height: ITEM_HEIGHT,
            top: y.value,
          });
          y.value += ITEM_HEIGHT;
        }
      }
    }
  }
  return items;
}

function displaySessionId(item: DisplayItem): string | null {
  if (item.type !== "session") return null;
  if (item.isChild) return item.session?.id ?? null;
  return item.group?.primarySessionId ?? null;
}

/**
 * Find the next rendered session row. When a collapsed lineage or section
 * hides the active session, treat its visible representative as the current
 * position.
 */
export function adjacentVisibleSessionId(
  displayItems: DisplayItem[],
  activeSessionId: string | null,
  delta: number,
): string | null {
  if (!activeSessionId) return null;

  let current = displayItems.findIndex((item) => displaySessionId(item) === activeSessionId);
  if (current < 0) {
    current = displayItems.findIndex((item) => item.memberSessionIds?.includes(activeSessionId));
  }
  if (current < 0) {
    current = displayItems.findIndex(
      (item) =>
        item.type === "session" &&
        item.group?.sessions.some((session) => session.id === activeSessionId),
    );
  }
  if (current < 0) {
    current = displayItems.findIndex(
      (item) =>
        item.type === "header" &&
        item.sectionGroups?.some((group) =>
          group.sessions.some((session) => session.id === activeSessionId),
        ),
    );
  }

  if (current < 0) {
    const rows = displayItems.filter((item) => item.type === "session");
    const edge = delta > 0 ? rows[0] : rows[rows.length - 1];
    return edge ? displaySessionId(edge) : null;
  }

  for (let next = current + delta; next >= 0 && next < displayItems.length; next += delta) {
    const id = displaySessionId(displayItems[next]!);
    if (id) return id;
  }
  return null;
}

/**
 * Compute total pixel height of the display items list.
 */
export function computeTotalSize(displayItems: DisplayItem[]): number {
  if (displayItems.length === 0) return 0;
  const last = displayItems[displayItems.length - 1]!;
  return last.top + last.height;
}

/**
 * Binary search for the index of the first visible item given
 * scrollY position.  Accounts for OVERSCAN rows before the
 * viewport.
 */
export function findStart(displayItems: DisplayItem[], scrollY: number): number {
  const target = scrollY - OVERSCAN * ITEM_HEIGHT;
  let lo = 0;
  let hi = displayItems.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    if (displayItems[mid]!.top + displayItems[mid]!.height <= target) {
      lo = mid + 1;
    } else {
      hi = mid;
    }
  }
  return Math.max(0, lo);
}
