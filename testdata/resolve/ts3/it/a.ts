import { vi } from 'vitest';
export async function f(){ const a = await vi.importActual<typeof import('./x')>('./x'); let b: typeof import('./y'); }
export function g(a: typeof import('./z')){}
