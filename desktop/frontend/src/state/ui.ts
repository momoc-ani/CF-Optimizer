import { create } from 'zustand';

export type PageKey = 'overview' | 'benchmark' | 'acceleration' | 'proxy' | 'routes' | 'ranges' | 'history' | 'logs' | 'settings';

interface UIState {
  page: PageKey;
  benchmarkFilter: string;
  logSearch: string;
  setPage: (page: PageKey) => void;
  setBenchmarkFilter: (value: string) => void;
  setLogSearch: (value: string) => void;
}

export const useUIStore = create<UIState>((set) => ({
  page: 'overview',
  benchmarkFilter: '',
  logSearch: '',
  setPage: (page) => set({ page }),
  setBenchmarkFilter: (benchmarkFilter) => set({ benchmarkFilter }),
  setLogSearch: (logSearch) => set({ logSearch }),
}));
