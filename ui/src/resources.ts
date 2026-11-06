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

// byName orders strings as Array.prototype.sort does: by UTF-16 code unit.
const byName = (a: string, b: string): number => (a < b ? -1 : a > b ? 1 : 0);

// groupByScope groups rs by account, then region: accounts ascending,
// regions ascending within each, resources in input order within each
// region; no group is empty.
export function groupByScope(rs: readonly Resource[]): readonly AccountGroup[] {
  const accounts = new Map<string, Map<string, Resource[]>>();
  for (const r of rs) {
    let regions = accounts.get(r.account);
    if (regions === undefined) {
      regions = new Map();
      accounts.set(r.account, regions);
    }
    const held = regions.get(r.region);
    if (held === undefined) {
      regions.set(r.region, [r]);
    } else {
      held.push(r);
    }
  }
  return [...accounts.keys()].sort(byName).map((account) => {
    const regions = accounts.get(account) ?? new Map<string, Resource[]>();
    return {
      account,
      regions: [...regions.keys()].sort(byName).map((region) => ({ region, resources: regions.get(region) ?? [] })),
    };
  });
}
