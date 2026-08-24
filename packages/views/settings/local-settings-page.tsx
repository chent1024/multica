"use client";

import React from "react";
import {
  Keyboard,
  ListTodo,
  SlidersHorizontal,
} from "lucide-react";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { useNavigation } from "../navigation";
import { CollapsedNavTrigger } from "../layout/page-header";
import { useT } from "../i18n";
import { PreferencesTab } from "./components/preferences-tab";
import { KeyboardShortcutsTab } from "./components/keyboard-shortcuts-tab";
import { IssueTab } from "./components/issue-tab";

export interface LocalSettingsExtraTab {
  value: string;
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  content: React.ReactNode;
}

const LOCAL_TABS = [
  { key: "preferences", value: "preferences", icon: SlidersHorizontal },
  { key: "shortcuts", value: "shortcuts", icon: Keyboard },
  { key: "issue", value: "issue", icon: ListTodo },
] as const;

const TAB_QUERY_KEY = "tab";
const TRIGGER_CLASS =
  "h-8 shrink-0 px-2.5 hover:bg-surface-hover data-active:!bg-surface-selected data-active:!text-surface-selected-foreground data-active:hover:!bg-surface-selected md:!w-full md:px-2 md:after:hidden";

export function LocalSettingsPage({
  extraTabs,
}: {
  extraTabs?: LocalSettingsExtraTab[];
}) {
  const { t } = useT("settings");
  const navigation = useNavigation();
  const isMobile = useIsMobile();
  const validTabs = React.useMemo(
    () => new Set([...LOCAL_TABS.map((tab) => tab.value), ...(extraTabs?.map((tab) => tab.value) ?? [])]),
    [extraTabs],
  );
  const candidate = navigation.searchParams.get(TAB_QUERY_KEY);
  const activeTab = candidate && validTabs.has(candidate) ? candidate : "preferences";

  const changeTab = (next: string) => {
    const params = new URLSearchParams(navigation.searchParams);
    params.set(TAB_QUERY_KEY, next);
    navigation.replace(`${navigation.pathname}?${params.toString()}`);
  };

  return (
    <Tabs
      value={activeTab}
      onValueChange={changeTab}
      orientation={isMobile ? "horizontal" : "vertical"}
      className="flex min-h-0 flex-1 flex-col gap-0 overflow-y-auto md:flex-row md:overflow-hidden"
    >
      <div className="shrink-0 overflow-x-auto border-b border-surface-border p-2 md:w-56 md:overflow-y-auto md:border-b-0 md:border-r md:p-4">
        <div className="flex items-center md:mb-4">
          <CollapsedNavTrigger />
          <h1 className="sr-only text-body font-semibold md:not-sr-only md:px-2">
            {t(($) => $.page.title)}
          </h1>
        </div>
        <TabsList variant="line" className="flex w-max min-w-full flex-row items-center gap-1 p-0 md:w-full md:flex-col md:items-stretch">
          <span className="hidden px-2 pb-1 pt-2 text-caption font-medium text-muted-foreground md:block">
            Local
          </span>
          {LOCAL_TABS.map(({ key, value, icon: Icon }) => (
            <TabsTrigger key={value} value={value} className={TRIGGER_CLASS}>
              <Icon className="h-4 w-4" />
              {t(($) => $.page.tabs[key])}
            </TabsTrigger>
          ))}
          {extraTabs?.map((tab) => (
            <TabsTrigger key={tab.value} value={tab.value} className={TRIGGER_CLASS}>
              <tab.icon className="h-4 w-4" />
              {tab.label}
            </TabsTrigger>
          ))}
        </TabsList>
      </div>

      <div className="min-w-0 flex-1 md:overflow-y-auto">
        <div className="mx-auto w-full max-w-3xl p-4 sm:p-6 md:p-8">
          <TabsContent value="preferences"><PreferencesTab /></TabsContent>
          <TabsContent value="shortcuts"><KeyboardShortcutsTab /></TabsContent>
          <TabsContent value="issue"><IssueTab /></TabsContent>
          {extraTabs?.map((tab) => (
            <TabsContent key={tab.value} value={tab.value}>{tab.content}</TabsContent>
          ))}
        </div>
      </div>
    </Tabs>
  );
}
