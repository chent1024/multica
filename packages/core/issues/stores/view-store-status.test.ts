// @vitest-environment node
import { describe, expect, it, beforeEach } from "vitest";
import { createStore, type StoreApi } from "zustand/vanilla";
import {
  DEFAULT_HIDDEN_STATUS_CATEGORIES,
  viewStorePersistOptions,
  viewStoreSlice,
  type IssueViewState,
} from "./view-store";
import { baselineFromQuery } from "../../issue-views/baseline";

/**
 * Column visibility and the status filter used to be the same field. That was
 * only ever correct while a category held exactly one status; since MUL-6243 a
 * category can hold several, and the two questions have different answers.
 */
describe("column visibility vs status filter", () => {
  let store: StoreApi<IssueViewState>;
  beforeEach(() => {
    store = createStore<IssueViewState>()((set) => viewStoreSlice(set));
  });

  it("starts with exception and terminal columns hidden", () => {
    expect(store.getState().hiddenStatusCategories).toEqual(
      DEFAULT_HIDDEN_STATUS_CATEGORIES,
    );
  });

  // The regression: hiding one column wrote the OTHER six built-in keys into
  // statusFilters, so the query then excluded every custom status too — hiding
  // Backlog silently dropped a QA card sitting in the In Review column.
  it("hiding a column does not touch the status filter", () => {
    store.getState().hideStatus("done");

    expect(store.getState().hiddenStatusCategories).toEqual([
      ...DEFAULT_HIDDEN_STATUS_CATEGORIES,
      "done",
    ]);
    expect(store.getState().statusFilters).toEqual([]);
  });

  it("showing a column restores it without inventing a filter", () => {
    store.getState().hideStatus("done");
    store.getState().showStatus("blocked");

    expect(store.getState().hiddenStatusCategories).toEqual([
      "backlog",
      "cancelled",
      "done",
    ]);
    expect(store.getState().statusFilters).toEqual([]);
  });

  it("hiding the same column twice is idempotent", () => {
    store.getState().hideStatus("done");
    store.getState().hideStatus("done");

    expect(store.getState().hiddenStatusCategories).toEqual([
      ...DEFAULT_HIDDEN_STATUS_CATEGORIES,
      "done",
    ]);
  });

  it("a custom status filter survives hiding and showing a column", () => {
    store.getState().toggleStatusFilter("qa");
    store.getState().hideStatus("done");
    store.getState().showStatus("done");

    expect(store.getState().statusFilters).toEqual(["qa"]);
  });

  it("reset restores the focused default columns", () => {
    store.getState().showStatus("backlog");
    store.getState().hideStatus("done");
    store.getState().clearFilters();

    expect(store.getState().hiddenStatusCategories).toEqual(
      DEFAULT_HIDDEN_STATUS_CATEGORIES,
    );
  });

  it("migrates existing views to the focused default once", () => {
    const options = viewStorePersistOptions("test");
    const migrated = options.migrate(
      { hiddenStatusCategories: ["done"] },
      0,
    );

    expect(migrated.hiddenStatusCategories).toEqual([
      "done",
      ...DEFAULT_HIDDEN_STATUS_CATEGORIES,
    ]);
  });

  it("does not reapply defaults after the v1 migration", () => {
    const options = viewStorePersistOptions("test");
    const persisted = { hiddenStatusCategories: [] } as unknown as IssueViewState;

    expect(options.migrate(persisted, 1)).toBe(persisted);
  });
});

describe("saved view baseline", () => {
  // The regression: the baseline dropped any status filter that was not one of
  // the 7 built-ins, so reopening a view saved with a custom status filter came
  // back showing MORE than it was saved with.
  it("keeps a custom status filter", () => {
    const baseline = baselineFromQuery({ statusFilters: ["in_review", "qa"] });

    expect(baseline.status.has("qa")).toBe(true);
    expect(baseline.status.has("in_review")).toBe(true);
  });

  it("still drops values it cannot represent", () => {
    const baseline = baselineFromQuery({ statusFilters: ["", "qa"] });

    expect([...baseline.status]).toEqual(["qa"]);
  });
});
