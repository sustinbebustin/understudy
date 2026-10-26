// A small element builder. Text always goes in as text nodes, never as
// markup, so nothing an event or an answer carries can become HTML; and no
// inline style or handler attribute is set, which the CSP refuses.

export type Child = Node | string | null | undefined | false;

export type Props = {
  readonly class?: string;
  readonly title?: string;
  readonly attrs?: Readonly<Record<string, string>>;
  readonly onclick?: () => void;
  readonly onsubmit?: () => void;
};

// h makes a tag element with props and children.
export function h<K extends keyof HTMLElementTagNameMap>(tag: K, props: Props = {}, ...children: readonly Child[]): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  if (props.class !== undefined) {
    el.className = props.class;
  }
  if (props.title !== undefined) {
    el.title = props.title;
  }
  for (const [name, value] of Object.entries(props.attrs ?? {})) {
    el.setAttribute(name, value);
  }
  const { onclick, onsubmit } = props;
  if (onclick !== undefined) {
    el.addEventListener("click", () => onclick());
  }
  if (onsubmit !== undefined) {
    el.addEventListener("submit", (ev) => {
      // The page handles every form itself; the CSP's form-action 'none'
      // refuses a real submission.
      ev.preventDefault();
      onsubmit();
    });
  }
  el.append(...kept(children));
  return el;
}

// fill replaces parent's children.
export function fill(parent: Element, ...children: readonly Child[]): void {
  parent.replaceChildren(...kept(children));
}

function kept(children: readonly Child[]): (Node | string)[] {
  return children.filter((c): c is Node | string => c !== null && c !== undefined && c !== false);
}
