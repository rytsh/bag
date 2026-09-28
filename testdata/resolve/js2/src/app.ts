import { onClick, onHover, helper, Model } from "./handlers";
import { useState } from "react";

const table = { click: onClick, hover: onHover };
const list = [helper];
setTimeout(onClick, 10);

function local() { return 0; }

export function run(cb: () => void) {
  [1, 2].map(helper);
  const onHover = 5;
  register(onHover);
  register(cb);
  register(Model);
  register(useState);
  try { x(); } catch (helper) { register(helper); }
  for (const local of [1]) { register(local); }
  register(local);
  const inner = (onClick) => register(onClick);
  if (true) { let helper = 1; register(helper); }
  return { onClick, local };
}

export const wrapped = wrap((a) => register(a), (b) => register(onClick));

function register(x: any) { return x; }
