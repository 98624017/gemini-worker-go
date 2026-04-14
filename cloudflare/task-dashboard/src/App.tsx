import { useState } from "react";
import { useAuth } from "./hooks/useAuth";
import { LoginPage } from "./components/LoginPage";
import { DashboardPage } from "./components/DashboardPage";
import type { TaskListItem } from "./api/client";

export function App() {
  const { apiKey, isAuthenticated, login, logout } = useAuth();
  const [initialItems, setInitialItems] = useState<TaskListItem[] | null>(null);

  function handleLogin(key: string, items: TaskListItem[]) {
    login(key);
    setInitialItems(items);
  }

  function handleLogout() {
    logout();
    setInitialItems(null);
  }

  function handleChangeApiKey(newKey: string) {
    login(newKey);
    setInitialItems(null); // Force reload with new key
  }

  if (!isAuthenticated || !apiKey) {
    return <LoginPage onLogin={handleLogin} />;
  }

  return (
    <DashboardPage
      apiKey={apiKey}
      initialItems={initialItems}
      onLogout={handleLogout}
      onChangeApiKey={handleChangeApiKey}
    />
  );
}
