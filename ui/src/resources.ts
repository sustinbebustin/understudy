// Resources from GET /resources, grouped by account and region.

export type Resource = {
  readonly service: string;
  readonly account: string;
  readonly region: string;
  readonly arn: string;
  readonly summary?: string;
};

export type RegionGroup = { readonly region: string; readonly resources: readonly Resource[] };

export type AccountGroup = { readonly account: string; readonly regions: readonly RegionGroup[] };

// groupByScope groups rs by account, then region. Not implemented yet: it
// returns no groups.
export function groupByScope(_rs: readonly Resource[]): readonly AccountGroup[] {
  return [];
}
