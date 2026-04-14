import { useState, useEffect, useCallback } from "react";
import { TopBar, type ViewMode } from "./TopBar";
import { TaskList } from "./TaskList";
import { TaskDetail } from "./TaskDetail";
import { GalleryView } from "./GalleryView";
import { useTasks } from "../hooks/useTasks";
import { useTaskDetail } from "../hooks/useTaskDetail";
import { useTheme } from "../hooks/useTheme";
import { ApiError, type TaskListItem } from "../api/client";
import type { StatusFilter } from "./FilterBar";

interface DashboardPageProps {
  apiKey: string;
  initialItems: TaskListItem[] | null;
  onLogout: () => void;
  onChangeApiKey: (newKey: string) => void;
}

export function DashboardPage({ apiKey, initialItems, onLogout, onChangeApiKey }: DashboardPageProps) {
  const { theme, toggleTheme } = useTheme();
  const tasks = useTasks(apiKey, initialItems);
  const taskDetail = useTaskDetail(apiKey);
  const [view, setView] = useState<ViewMode>("tasks");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [showDetail, setShowDetail] = useState(false);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [modelFilter, setModelFilter] = useState("");

  useEffect(() => {
    // Skip if we already have data from login validation
    if (initialItems !== null) return;
    tasks.load().catch((err) => {
      if (err instanceof ApiError && err.status === 401) onLogout();
    });
  }, [apiKey]); // eslint-disable-line react-hooks/exhaustive-deps

  const handleRefresh = useCallback(() => {
    tasks.load().catch((err) => {
      if (err instanceof ApiError && err.status === 401) onLogout();
    });
  }, [tasks.load, onLogout]);

  const handleLoadMore = useCallback(() => {
    tasks.loadMore();
  }, [tasks.loadMore]);

  const handleSelect = useCallback(
    (taskId: string) => {
      setSelectedId(taskId);
      setShowDetail(true);
      taskDetail.load(taskId);
    },
    [taskDetail.load]
  );

  const handleBack = useCallback(() => {
    setShowDetail(false);
    taskDetail.clear();
  }, [taskDetail.clear]);

  const handleDetailRetry = useCallback(() => {
    if (selectedId) taskDetail.load(selectedId);
  }, [selectedId, taskDetail.load]);

  return (
    <div className="h-screen flex flex-col bg-base-200">
      <TopBar
        theme={theme}
        view={view}
        apiKey={apiKey}
        onToggleTheme={toggleTheme}
        onViewChange={setView}
        onChangeApiKey={onChangeApiKey}
        onLogout={onLogout}
      />

      {view === "tasks" ? (
        <div className="flex-1 flex overflow-hidden">
          <div
            className={`w-full lg:w-[320px] xl:w-[360px] border-r border-base-300 bg-base-100 flex-shrink-0 ${
              showDetail ? "hidden lg:flex lg:flex-col" : "flex flex-col"
            }`}
          >
            <TaskList
              items={tasks.items}
              selectedId={selectedId}
              loading={tasks.loading}
              loadingMore={tasks.loadingMore}
              hasMore={tasks.hasMore}
              error={tasks.error}
              statusFilter={statusFilter}
              modelFilter={modelFilter}
              onStatusFilterChange={setStatusFilter}
              onModelFilterChange={setModelFilter}
              onSelect={handleSelect}
              onRefresh={handleRefresh}
              onLoadMore={handleLoadMore}
            />
          </div>
          <div
            className={`flex-1 bg-base-100 ${
              showDetail ? "flex flex-col" : "hidden lg:flex lg:flex-col"
            }`}
          >
            <TaskDetail
              detail={taskDetail.detail}
              loading={taskDetail.loading}
              error={taskDetail.error}
              onRetry={handleDetailRetry}
              onBack={handleBack}
            />
          </div>
        </div>
      ) : (
        <div className="flex-1 overflow-y-auto bg-base-100">
          <GalleryView
            apiKey={apiKey}
            taskItems={tasks.items}
            modelFilter={modelFilter}
          />
        </div>
      )}
    </div>
  );
}
