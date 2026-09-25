/** Local, occurrence-level session search with a stable navigation cursor. */
import { untrack } from "svelte";
import type { DbMessage as Message } from "../api/generated/index.js";
import { buildSessionIndex, type Match, type SessionIndex } from "../search/session-index.js";
import {
  cursorFor,
  matchesInDisplayOrder,
  resolveSearchMatch,
  sameCursor,
  stepSearchMatch,
  type SearchCursor,
} from "../search/navigation.js";
import {
  blockTypeForKind,
  keepsAnswerBeforeTrailingTools,
  projectSessionScope,
  type SessionScope,
} from "../search/session-scope.js";
import type { BlockType } from "./ui.svelte.js";
import { messages } from "./messages.svelte.js";
import { sessions } from "./sessions.svelte.js";
import { settings } from "./settings.svelte.js";
import { ui } from "./ui.svelte.js";

export type { SearchCursor } from "../search/navigation.js";

export interface SearchMessageSource {
  sessionId: string | null;
  messages: Message[];
  loading: boolean;
  hasOlder: boolean;
  loadingOlder: boolean;
  historyComplete: boolean;
  ensureHistoryLoaded(): Promise<void>;
  ensureOrdinalLoaded(ordinal: number): Promise<void>;
}

export interface SearchView {
  selectedOrdinal: number | null;
  sortNewestFirst: boolean;
  selectOrdinal(ordinal: number): void;
  setFollowLatest(follow: boolean): void;
  /** Display projection inputs; absent means unfiltered normal mode. */
  transcriptMode?: "normal" | "focused";
  /**
   * Saved block-visibility set. Writers MUST reassign the Set rather than
   * mutate it in place: the filter-diff effect below snapshots the previous
   * reference, so in-place mutation is not observable as a change.
   */
  visibleBlocks?: ReadonlySet<BlockType>;
  hasBlockFilters?: boolean;
  renderUnknownXmlBlocksAsPreformatted?: boolean;
  /** Saved block-visibility predicate; preferred over `visibleBlocks` when defined. */
  isBlockVisible?(type: BlockType): boolean;
}

const EMPTY_MATCHES: Match[] = [];
const EMPTY_BLOCK_TYPES: ReadonlySet<BlockType> = new Set();

export class InSessionSearchStore {
  isOpen = $state(false);
  query = $state("");
  debouncedQuery = $state("");
  composing = $state(false);
  wholeWord = $state(false);
  current: SearchCursor | null = $state.raw(null);
  navigationRevision = $state(0);
  anchorOrdinal: number | null = $state(null);
  focusRequest = $state(0);
  resultsOpen = $state(false);
  private historyFailed = $state(false);
  /** Types the user manually hid while the find view is open. */
  private suppressedTypes = $state(new Set<BlockType>());

  private source: SearchMessageSource = messages;
  private view: SearchView = ui;
  private disposeEffects: () => void;
  private previousSessionId: string | null;
  private historyRequest: { sessionId: string; promise: Promise<void> } | null = null;
  /** Snapshot the `visibleBlocks` diff effect compares against each run. */
  private previousVisibleBlocks: ReadonlySet<BlockType> | undefined;

  private historyIncomplete = $derived(
    this.source.hasOlder || this.source.historyComplete === false,
  );
  historyError = $derived(this.isOpen && this.historyIncomplete && this.historyFailed);
  isActive = $derived(this.isOpen && this.debouncedQuery.trim() !== "");
  /**
   * The mode's saved-filter projection, shared by navigation and search.
   * `displayItems`/`normalItems` follow the saved filter; `messages` is the
   * filter-independent membership the index searches.
   */
  scope: SessionScope | null = $derived.by(() => {
    if (!this.source.sessionId) return null;
    return projectSessionScope({
      messages: this.source.messages,
      sessionId: this.source.sessionId,
      transcriptMode: this.view.transcriptMode,
      visibleBlocks: this.view.visibleBlocks,
      hasBlockFilters: this.view.hasBlockFilters,
      keepAnswerBeforeTrailingTools: keepsAnswerBeforeTrailingTools(
        settings.sessionProviders,
        sessions.activeSession?.agent,
      ),
    });
  });
  index: SessionIndex | null = $derived.by(() => {
    // The session is part of the dependency graph even when its message array
    // is temporarily shared with the previous session during a load transition.
    if (!this.source.sessionId || !this.isActive) return null;
    const scope = this.scope;
    if (!scope) return null;
    // Every searchable block of every eligible message is indexed; block
    // filters shape what renders, not what the search can find.
    return buildSessionIndex(
      scope.messages,
      this.debouncedQuery,
      { renderUnknownXmlBlocksAsPreformatted: this.view.renderUnknownXmlBlocksAsPreformatted },
      this.wholeWord,
    );
  });
  matches: Match[] = $derived(this.index?.matches ?? EMPTY_MATCHES);
  orderedMatches: readonly Match[] = $derived(
    matchesInDisplayOrder(this.matches, this.view.sortNewestFirst),
  );
  /**
   * Saved-hidden block types that own at least one active match, temporarily
   * revealed for the open find view. Suppressed types stay hidden; closing
   * search clears both reveals and suppression. Never persisted.
   */
  revealedBlockTypes: ReadonlySet<BlockType> = $derived.by(() => {
    if (!this.isOpen) return EMPTY_BLOCK_TYPES;
    const saved = this.view.visibleBlocks;
    if (saved === undefined) return EMPTY_BLOCK_TYPES;
    let revealed: Set<BlockType> | null = null;
    for (const match of this.matches) {
      const type = blockTypeForKind(match.kind, match.role);
      if (saved.has(type) || this.suppressedTypes.has(type)) continue;
      (revealed ??= new Set<BlockType>()).add(type);
    }
    return revealed ?? EMPTY_BLOCK_TYPES;
  });
  total = $derived(this.index?.total ?? 0);
  loadingHistory = $derived(
    this.isOpen &&
      (this.source.loading ||
        this.source.loadingOlder ||
        (this.historyIncomplete && !this.historyError)),
  );
  resolvedCurrent: Match | null = $derived(
    resolveSearchMatch(
      this.orderedMatches,
      this.current,
      this.anchorOrdinal,
      this.view.sortNewestFirst,
    ),
  );
  currentIndex = $derived.by(() => {
    const current = this.resolvedCurrent;
    return current ? this.orderedMatches.indexOf(current) : -1;
  });

  constructor(source: SearchMessageSource = messages, view: SearchView = ui) {
    this.source = source;
    this.view = view;
    this.previousSessionId = source.sessionId;
    this.disposeEffects = $effect.root(() => {
      $effect(() => {
        const sessionId = this.source.sessionId;
        untrack(() => {
          if (sessionId === this.previousSessionId) return;
          this.previousSessionId = sessionId;
          this.current = null;
          this.anchorOrdinal = null;
          this.composing = false;
          this.historyRequest = null;
          this.historyFailed = false;
          this.suppressedTypes = new Set();
          this.navigationRevision++;
          if (!sessionId) this.close();
        });
      });

      $effect(() => {
        const query = this.query;
        const open = this.isOpen;
        const sessionId = this.source.sessionId;
        const composing = this.composing;
        if (!open || !sessionId) {
          untrack(() => this.applyQuery(""));
          return;
        }
        if (composing) return;
        if (!query.trim()) {
          untrack(() => this.applyQuery(""));
          return;
        }
        if (query === untrack(() => this.debouncedQuery)) return;
        const timer = setTimeout(() => {
          if (
            this.isOpen &&
            !this.composing &&
            this.source.sessionId === sessionId &&
            this.query === query
          ) {
            this.applyQuery(query);
          }
        }, 150);
        return () => clearTimeout(timer);
      });

      $effect(() => {
        const saved = this.view.visibleBlocks;
        const open = this.isOpen;
        untrack(() => {
          const previous = this.previousVisibleBlocks;
          this.previousVisibleBlocks = saved ? new Set(saved) : saved;
          if (!open || !previous || !saved) return;
          // A manual hide while the find view is open suppresses the
          // temporary reveal; re-enabling clears that suppression.
          for (const type of previous) {
            if (!saved.has(type)) this.noteManualBlockFilterChange(type, false);
          }
          for (const type of saved) {
            if (!previous.has(type)) this.noteManualBlockFilterChange(type, true);
          }
        });
      });

      $effect(() => {
        const open = this.isOpen;
        const sessionId = this.source.sessionId;
        const incomplete = this.historyIncomplete;
        const loading = this.source.loading;
        if (open && sessionId && incomplete && !loading) {
          untrack(() => {
            if (!this.historyFailed) this.requestHistory();
          });
        }
      });

      $effect(() => {
        const match = this.resolvedCurrent;
        if (!match) return;
        untrack(() => {
          if (this.current && sameCursor(this.current, match)) return;
          // Pin the first implicit match too. Loading older pages must not
          // silently move it, even before the first explicit next/previous.
          this.current = cursorFor(match);
          this.navigationRevision++;
          this.selectCurrent();
        });
      });
    });
  }

  private applyQuery(query: string): void {
    if (query === this.debouncedQuery) return;
    this.debouncedQuery = query;
    this.current = null;
    this.navigationRevision++;
    this.selectCurrent();
  }

  private selectCurrent(): void {
    const match = this.resolvedCurrent;
    if (!match) return;
    if (!this.current || !sameCursor(this.current, match)) {
      this.current = cursorFor(match);
    }
    this.view.selectOrdinal(match.ordinal);
    this.view.setFollowLatest(false);
  }

  private requestHistory(): void {
    const sessionId = this.source.sessionId;
    if (!sessionId || !this.isOpen || !this.historyIncomplete || this.source.loading) return;
    if (this.historyRequest?.sessionId === sessionId) return;
    this.historyFailed = false;
    const promise: Promise<void> = Promise.resolve().then(() => {
      if (
        this.historyRequest?.promise === promise &&
        this.source.sessionId === sessionId &&
        this.isOpen
      ) {
        return this.source.ensureHistoryLoaded();
      }
    });
    this.historyRequest = { sessionId, promise };
    void promise
      .catch((error: unknown) => {
        if (this.historyRequest?.promise === promise && this.source.sessionId === sessionId) {
          console.warn("Could not load session history for search", error);
        }
      })
      .finally(() => {
        if (this.historyRequest?.promise !== promise) return;
        this.historyRequest = null;
        // The shared message loader can handle its own rejection. Remaining
        // history still means these counts are partial and an explicit retry is
        // needed; do not leave the find bar announcing loading indefinitely.
        if (this.source.sessionId === sessionId) {
          this.historyFailed = this.historyIncomplete;
        }
      });
  }

  retryHistory(): void {
    this.requestHistory();
  }

  countForBlock(key: string | undefined): number {
    return key ? (this.index?.byBlock.get(key) ?? 0) : 0;
  }

  countForOrdinal(ordinal: number): number {
    return this.index?.byOrdinal.get(ordinal) ?? 0;
  }

  isCurrentBlock(key: string | undefined): boolean {
    return !!key && this.resolvedCurrent?.blockKey === key;
  }

  currentOccurrence(key: string | undefined): number {
    return this.isCurrentBlock(key) ? this.resolvedCurrent!.occurrence : -1;
  }

  open(): void {
    if (!this.isOpen) {
      this.anchorOrdinal = this.view.selectedOrdinal;
      this.current = null;
      this.navigationRevision++;
      this.isOpen = true;
    }
    this.focusRequest++;
    this.requestHistory();
  }

  /** Closing hides search without erasing the last query. */
  close(): void {
    this.isOpen = false;
    this.composing = false;
    this.debouncedQuery = "";
    this.current = null;
    this.resultsOpen = false;
    this.suppressedTypes = new Set();
    this.navigationRevision++;
  }

  /**
   * Records a saved-filter change while the find view is open. Hiding a type
   * suppresses its temporary reveal until it is re-enabled or the view
   * closes; re-enabling clears the suppression. Ignored while closed.
   */
  noteManualBlockFilterChange(type: BlockType, visible: boolean): void {
    if (!this.isOpen) return;
    if (visible === !this.suppressedTypes.has(type)) return;
    // Reassign rather than mutate: the visibleBlocks diff effect works on
    // reference snapshots, so reassignment is the explicit invalidation
    // signal consumers of this set rely on.
    const next = new Set(this.suppressedTypes);
    if (visible) {
      next.delete(type);
    } else {
      next.add(type);
    }
    this.suppressedTypes = next;
  }

  /**
   * Saved block visibility plus the open search's temporary reveals. Must
   * agree with `isTranscriptBlockVisible` in session-scope.ts, which applies
   * the same saved-or-revealed rule inside the scope projection.
   */
  isBlockEffectivelyVisible(type: BlockType): boolean {
    const saved = this.view.visibleBlocks;
    const savedVisible = this.view.isBlockVisible
      ? this.view.isBlockVisible(type)
      : saved === undefined || saved.has(type);
    return savedVisible || this.revealedBlockTypes.has(type);
  }

  toggleWholeWord(): void {
    this.wholeWord = !this.wholeWord;
    this.current = null;
    this.navigationRevision++;
    this.selectCurrent();
  }

  clearQuery(): void {
    this.query = "";
    this.applyQuery("");
  }

  toggle(): void {
    if (this.isOpen) this.close();
    else this.open();
  }

  goTo(cursor: SearchCursor): void {
    // A result panel can remain visible during debounce. Its old rows cannot
    // navigate a different query that has already been entered in the input.
    if (!this.isOpen || this.composing || this.query !== this.debouncedQuery) return;
    const match = this.matches.find((candidate) => sameCursor(candidate, cursor));
    if (!match) return;
    this.current = cursorFor(match);
    this.navigationRevision++;
    this.selectCurrent();
  }

  private step(delta: 1 | -1): void {
    if (!this.isOpen || this.composing) return;
    const query = this.query.trim() ? this.query : "";
    const freshQuery = query !== this.debouncedQuery;
    if (freshQuery) this.applyQuery(query);
    const match = stepSearchMatch(this.orderedMatches, this.resolvedCurrent, delta, freshQuery);
    if (match) this.goTo(match);
  }

  next(): void {
    this.step(1);
  }
  prev(): void {
    this.step(-1);
  }

  get currentOrdinal(): number | null {
    return this.resolvedCurrent?.ordinal ?? null;
  }
  get loading(): boolean {
    return (
      this.loadingHistory ||
      (this.isOpen && !!this.query.trim() && (this.composing || this.query !== this.debouncedQuery))
    );
  }

  /** Release effects for isolated store instances and embedded session views. */
  destroy(): void {
    this.disposeEffects();
    this.historyRequest = null;
    this.close();
  }
}

export const inSessionSearch = new InSessionSearchStore();
