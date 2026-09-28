import { Base, Shape, util } from './base';
import Def from './base';
import * as B from './base';
export class Child extends Base implements Shape {
  field: Base;
  area(): number { return util(1); }
  make(s: Shape): Base { return new Base(); }
  run() { const b = new Base(); b.hello(); B.util(2); const d = new Def(); }
}
export function top(x: Child) { x.area(); }
