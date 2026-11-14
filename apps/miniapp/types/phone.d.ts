// Types for phone mini-programs (JS logic + phone.Ui view). Mirrors
// fw/ui/proto.go; the host strips types at load time, tsc checks them here.

type Style = "" | "dim" | "accent" | "title" | "ok" | "warn" | "big";

interface UiAction {
  id: string;
  label: string;
  key?: string;
  primary?: boolean;
  group?: "item" | "page" | "danger";
  confirm?: string;
}

interface UiNodeBase {
  id?: string;
  actions?: UiAction[];
}

interface TextNode extends UiNodeBase { kind: "text"; text: string; style?: Style }
interface SectionNode extends UiNodeBase { kind: "section"; text: string }
interface DocNode extends UiNodeBase { kind: "doc"; text: string }
interface ItemNode extends UiNodeBase {
  kind: "item";
  title: string;
  subtitle?: string;
  trailing?: string;
  badge?: string;
  icon?: string;
  src?: string;
  rows?: number;
  style?: "" | "bubble-in" | "bubble-out";
}
interface InputNode extends UiNodeBase {
  kind: "input";
  placeholder?: string;
  value?: string;
  secret?: boolean;
  submit?: string;
  clear?: boolean;
}
interface ButtonNode extends UiNodeBase { kind: "button"; text: string }
interface ImageNode extends UiNodeBase { kind: "image"; src: string; rows?: number; label?: string; secure?: boolean }
/** A bar filled to fraction (0-1); text is a label on the left, trailing a note on the right. */
interface ProgressNode extends UiNodeBase { kind: "progress"; fraction: number; text?: string; trailing?: string }

type UiNode = TextNode | SectionNode | DocNode | ItemNode | InputNode | ButtonNode | ImageNode | ProgressNode;

interface ActionEvent {
  action: string;
  node: string;
  value: string;
  fields: Record<string, string>;
}

interface PageThis<D> {
  data: D;
  setData(patch: Partial<D>): void;
}

interface PageDef<D> {
  title?: string;
  data?: D;
  actions?: UiAction[];
  onLoad?(query: Record<string, any>): void;
  render(): UiNode[];
  onAction?(e: ActionEvent): void;
}

declare function Page<D = Record<string, never>>(name: string, def: PageDef<D> & ThisType<PageThis<D>>): void;

interface Fail { errMsg: string }

declare const phone: {
  /**
   * msg in the device's language: its translation from the mini-program's
   * own locale/<lang>.json ({"English message": "translation"}; lang is
   * PHONE_LANG, default zh-Hans), or msg itself when there is none. With
   * args, %s and %d are replaced by them in order (%s: String(arg), %d:
   * Number(arg) truncated to an integer) and %% is a percent sign; without
   * args the text is returned as it is.
   */
  t(msg: string, ...args: (string | number)[]): string;
  toast(text: string): void;
  navigateTo(page: string, query?: Record<string, unknown>): void;
  navigateBack(delta?: number): void;
  getStorage<T = unknown>(key: string): T | undefined;
  setStorage(key: string, value: unknown): void;
  scanCode(opts: { success?(r: { result: string }): void; fail?(e: Fail): void }): void;
  requestPayment(opts: {
    merchant: string;
    amount: number;
    success?(r: { orderId: string; verified: boolean }): void;
    fail?(e: Fail): void;
  }): void;
};

declare const ui: {
  text(t: string, style?: Style): TextNode;
  section(t: string): SectionNode;
  doc(md: string, id?: string): DocNode;
  item(o: Omit<ItemNode, "kind">): ItemNode;
  input(o: Omit<InputNode, "kind">): InputNode;
  image(src: string, rows?: number): ImageNode;
  progress(fraction: number, note?: string, label?: string): ProgressNode;
  button(id: string, label: string): ButtonNode;
  primary(id: string, label: string): UiAction;
  action(id: string, label: string, key?: string): UiAction;
  danger(id: string, label: string, confirm?: string): UiAction;
};

// JSX (app.tsx): elements are phone.Ui nodes, text children become the
// node's text. Compile with "jsx": "react", "jsxFactory": "h",
// "jsxFragmentFactory": "Fragment".
declare function h(type: string | ((props: any) => UiNode | UiNode[]), props: Record<string, unknown> | null, ...children: unknown[]): UiNode;
declare function Fragment(props: { children?: unknown[] }): UiNode[];

declare namespace JSX {
  type Element = UiNode;
  interface ElementChildrenAttribute { children: {} }
  interface IntrinsicElements {
    text: { style?: Style; children?: unknown };
    section: { children?: unknown };
    doc: { id?: string; children?: unknown };
    item: Omit<ItemNode, "kind">;
    input: Omit<InputNode, "kind">;
    image: { src: string; rows?: number };
    progress: { fraction: number; trailing?: string; children?: unknown };
    button: { id: string; action?: string; children?: unknown };
  }
}
