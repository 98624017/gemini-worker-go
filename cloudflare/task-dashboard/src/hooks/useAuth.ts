import { useState, useCallback } from "react";

const STORAGE_KEY = "task-dashboard-api-key";

export function useAuth() {
  const [apiKey, setApiKeyState] = useState<string | null>(() =>
    localStorage.getItem(STORAGE_KEY)
  );

  const login = useCallback((key: string) => {
    localStorage.setItem(STORAGE_KEY, key);
    setApiKeyState(key);
  }, []);

  const logout = useCallback(() => {
    localStorage.removeItem(STORAGE_KEY);
    setApiKeyState(null);
  }, []);

  return {
    apiKey,
    isAuthenticated: apiKey !== null && apiKey !== "",
    login,
    logout,
  };
}
