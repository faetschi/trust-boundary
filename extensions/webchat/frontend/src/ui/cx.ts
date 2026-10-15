// Joins class names, skipping falsy entries. Three lines on purpose: no runtime dependency.
export function cx(...names: ReadonlyArray<string | false | null | undefined>): string {
  return names.filter(Boolean).join(" ");
}
