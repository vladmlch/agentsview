/**
 * Session-scoped expansion state for assistant turns.
 *
 * Manual per-key overrides sit above a session-wide bulk baseline set by
 * `expandAll`/`collapseAll`; both sit above the caller's default (which is
 * where the auto-collapse preference feeds in). User-prompt disclosures
 * are manual-only — bulk actions never touch them. Nothing here persists;
 * `activateSession` drops everything when the session changes.
 *
 * Maps are reassigned rather than mutated so `$derived` consumers see each
 * change as a new value.
 */

export type BulkBaseline = "expand" | "collapse" | null;

/** Returns a new map with `key` set to `value`; the input is left untouched.
 *  Returns the same map when nothing would change so callers can assign the
 *  result unconditionally without spurious `$state` invalidation. */
function withEntry<K>(map: Map<K, boolean>, key: K, value: boolean): Map<K, boolean> {
  if (map.get(key) === value) return map;
  const next = new Map(map);
  next.set(key, value);
  return next;
}

/**
 * Expansion state resolver for one active session.
 *
 * Methods are unbound — pass wrappers to components instead of the method
 * itself, e.g. `(key) => turnCollapse.isTurnExpanded(key, !ui.autoCollapseAssistantTurns)`.
 *
 * Each `is*Expanded` caller owns the `defaultExpanded` polarity: the
 * auto-collapse preference being `true` means "collapsed by default", so
 * callers pass `false` (and `!ui.autoCollapseAssistantTurns` for rows that
 * follow the preference). Overrides and the bulk baseline then sit on top.
 */
export class TurnCollapseStore {
  private activeSessionId = $state<string | null>(null);
  private baseline: BulkBaseline = $state(null);
  private turnOverrides = $state(new Map<string, boolean>());
  private eventOverrides = $state(new Map<string, boolean>());
  private toolSectionOverrides = $state(new Map<string, boolean>());
  private promptOverrides = $state(new Map<number, boolean>());

  get sessionId(): string | null {
    return this.activeSessionId;
  }

  /** Direction of the last `expandAll`/`collapseAll`, or `null` when no
   *  bulk action has run this session. Drives bulk-control labelling. */
  get bulkBaseline(): BulkBaseline {
    return this.baseline;
  }

  /** Switches the active session. A real change clears every override, the
   *  bulk baseline, and prompt disclosures; re-activating the current
   *  session is a no-op so projection rebuilds do not wipe state.
   *  `activateSession(null)` always resets — writes made while no session
   *  is active must not leak into the next activation. */
  activateSession(sessionId: string | null): void {
    if (sessionId !== null && sessionId === this.activeSessionId) return;
    this.activeSessionId = sessionId;
    this.baseline = null;
    this.turnOverrides = new Map();
    this.eventOverrides = new Map();
    this.toolSectionOverrides = new Map();
    this.promptOverrides = new Map();
  }

  isTurnExpanded(key: string, defaultExpanded: boolean): boolean {
    return this.turnOverrides.get(key) ?? this.baselineExpanded() ?? defaultExpanded;
  }

  isEventExpanded(key: string, defaultExpanded: boolean): boolean {
    return this.eventOverrides.get(key) ?? this.baselineExpanded() ?? defaultExpanded;
  }

  isToolSectionExpanded(key: string, defaultExpanded: boolean): boolean {
    return this.toolSectionOverrides.get(key) ?? this.baselineExpanded() ?? defaultExpanded;
  }

  /** Prompt disclosures ignore the bulk baseline and default to collapsed. */
  isUserPromptExpanded(messageId: number): boolean {
    return this.promptOverrides.get(messageId) ?? false;
  }

  setTurnExpanded(key: string, expanded: boolean): void {
    this.turnOverrides = withEntry(this.turnOverrides, key, expanded);
  }

  setEventExpanded(key: string, expanded: boolean): void {
    this.eventOverrides = withEntry(this.eventOverrides, key, expanded);
  }

  setToolSectionExpanded(key: string, expanded: boolean): void {
    this.toolSectionOverrides = withEntry(this.toolSectionOverrides, key, expanded);
  }

  setUserPromptExpanded(messageId: number, expanded: boolean): void {
    this.promptOverrides = withEntry(this.promptOverrides, messageId, expanded);
  }

  expandAll(): void {
    this.applyBulkBaseline("expand");
  }

  collapseAll(): void {
    this.applyBulkBaseline("collapse");
  }

  /** Carries a turn's manual override across a re-anchored key when loading
   *  older pages extends a partial leading turn. */
  migrateTurnKey(oldKey: string, newKey: string): void {
    if (oldKey === newKey) return;
    const value = this.turnOverrides.get(oldKey);
    if (value === undefined) return;
    const next = new Map(this.turnOverrides);
    next.delete(oldKey);
    next.set(newKey, value);
    this.turnOverrides = next;
  }

  private baselineExpanded(): boolean | null {
    if (this.baseline === "expand") return true;
    if (this.baseline === "collapse") return false;
    return null;
  }

  /** Bulk actions replace the baseline and drop earlier manual exceptions;
   *  a later click creates a fresh override. Prompt disclosures stay put. */
  private applyBulkBaseline(baseline: Exclude<BulkBaseline, null>): void {
    this.baseline = baseline;
    this.turnOverrides = new Map();
    this.eventOverrides = new Map();
    this.toolSectionOverrides = new Map();
  }
}

export const turnCollapse = new TurnCollapseStore();
