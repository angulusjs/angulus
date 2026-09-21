import type { CheckEvent } from "./vite.mjs";

export interface LibraryBuildOptions {
  root?: string;
  entry?: string;
  onEvent?: (event: CheckEvent) => void;
}

export interface LibraryBuildResult {
  directory: string;
  entry: string;
  types: string;
  style?: string;
}

export declare function buildLibrary(options?: LibraryBuildOptions): Promise<LibraryBuildResult>;
