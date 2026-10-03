export interface AngulusSourceMap {
  version: number;
  file?: string;
  sources: string[];
  sourcesContent?: (string | null)[];
  names: string[];
  mappings: string;
}

export type SourceMapInput = AngulusSourceMap | string | null;
