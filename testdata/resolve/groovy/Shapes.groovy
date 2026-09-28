package com.y
import java.util.List
import static java.lang.Math.max
import com.z.*

@Deprecated
abstract class A<T> extends B<T> implements C, D {
    static final int N = 1
    abstract void doIt()
    List<String> items() { return [] }
    def go() {
        new Helper().help()
        Util.stat(1)
        this.doIt()
        items().each { x -> println x }
        def h = new Helper()
        h.help()
    }
}
interface C extends E {}
enum Color { RED, GREEN }
trait T1 { def hi() {} }
class Helper { void help() {} }
def top() { 1 }
