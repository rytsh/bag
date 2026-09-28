import { registerRoot } from "remotion";
import { Root } from "./Root";

registerRoot(Root);
const { pc } = await import('./lazy');
export const w = async <T>({ a, }: { a: T }): Promise<T> => {
  return helperFn(a);
};
const helperFn = <T,>(a: T) => a;
