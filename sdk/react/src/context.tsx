import React, { createContext, useContext, type ReactNode } from "react";
import { TridentClient, type TridentClientConfig } from "@trident-indexer/sdk";

interface TridentContextValue {
  client: TridentClient;
}

const TridentContext = createContext<TridentContextValue | null>(null);

export interface TridentProviderProps {
  apiUrl: string;
  apiKey: string;
  network?: TridentClientConfig["network"];
  children: ReactNode;
}

export function TridentProvider({
  apiUrl,
  apiKey,
  network = "mainnet",
  children,
}: TridentProviderProps) {
  // Stable client reference — only rebuilt when config props change.
  const client = React.useMemo(
    () => new TridentClient({ apiUrl, apiKey, network }),
    [apiUrl, apiKey, network],
  );

  return (
    <TridentContext.Provider value={{ client }}>
      {children}
    </TridentContext.Provider>
  );
}

export function useTridentClient(): TridentClient {
  const ctx = useContext(TridentContext);
  if (!ctx) {
    throw new Error("useTridentClient must be used inside <TridentProvider>");
  }
  return ctx.client;
}
