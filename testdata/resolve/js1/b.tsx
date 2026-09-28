import { A } from './a';
interface P { x: number }
class Q extends A implements P { f(p: P): A { return new A(); } }
