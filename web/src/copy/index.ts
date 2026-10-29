import en from "./en.json";

type Leaves<T, P extends string = ""> = {
  [K in keyof T & string]: T[K] extends string ? `${P}${K}` : Leaves<T[K], `${P}${K}.`>;
}[keyof T & string];

export type CopyKey = Leaves<typeof en>;

/** Returns the copy string for key, with {name} values filled in. */
export function t(key: CopyKey, values?: Record<string, string | number>): string {
  let node: unknown = en;
  for (const part of key.split(".")) {
    node = (node as Record<string, unknown>)[part];
  }
  let text = typeof node === "string" ? node : key;
  if (values) {
    for (const [k, v] of Object.entries(values)) {
      text = text.replaceAll(`{${k}}`, String(v));
    }
  }
  return text;
}
