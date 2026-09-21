import type { Plugin } from "vite";

export interface Diagnostic {
  file: string;
  start: number;
  end: number;
  line: number;
  column: number;
  code: string;
  message: string;
  severity: "error" | "warning";
}

export type CheckEvent =
  | { version: 1; type: "checking"; revision: number }
  | {
      version: 1;
      type: "checked";
      revision: number;
      diagnostics: Diagnostic[];
      valid: boolean;
      stale: boolean;
    };

export interface AngulusPluginOptions {
  onEvent?: (event: CheckEvent) => void;
  checkBuild?: boolean;
}

export declare function angulus(options?: AngulusPluginOptions): Plugin;
export default angulus;
