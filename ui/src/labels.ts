// Human labels for the wire names in labels.gen.ts. Every generated name
// needs one; an unknown name renders as itself.

export const kindLabels: Readonly<Record<string, string>> = {};
export const viaLabels: Readonly<Record<string, string>> = {};
export const mechanismLabels: Readonly<Record<string, string>> = {};
export const idKindLabels: Readonly<Record<string, string>> = {};

// label returns table's label for name, or name when it has none. Not
// implemented yet: it returns "".
export function label(_table: Readonly<Record<string, string>>, _name: string): string {
  return "";
}
