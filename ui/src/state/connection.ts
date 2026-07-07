// Connection settings entered in onboarding, persisted to localStorage so an
// operator configures the Stem once and never touches .env.

import { create } from "zustand";
import { persist } from "zustand/middleware";
import type { StemConnection } from "../lib/api";

interface ConnectionState {
  configured: boolean;
  baseUrl: string;
  apiKey: string;
  operatorName: string;
  configure: (settings: {
    baseUrl: string;
    apiKey: string;
    operatorName: string;
  }) => void;
  reset: () => void;
}

export const useConnection = create<ConnectionState>()(
  persist(
    (set) => ({
      configured: false,
      baseUrl: "",
      apiKey: "",
      operatorName: "",
      configure: ({ baseUrl, apiKey, operatorName }) =>
        set({ configured: true, baseUrl, apiKey, operatorName }),
      reset: () =>
        set({ configured: false, baseUrl: "", apiKey: "", operatorName: "" }),
    }),
    { name: "opentendril.connection" },
  ),
);

export function currentConnection(): StemConnection {
  const { baseUrl, apiKey } = useConnection.getState();
  return { baseUrl: baseUrl.replace(/\/+$/, ""), apiKey };
}
