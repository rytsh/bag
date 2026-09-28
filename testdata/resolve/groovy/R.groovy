package r
import groovy.transform.CompileStatic
@CompileStatic
class R1 {
    @Override
    String toString() { return "x" }
    void a() {
        list.each { it.go() }
        items.collect { x -> x.run() }
        foo(bar())
        new Helper().help()
        Helper.make()
        def h = new Helper(1)
        h?.help()
    }
    static class Inner {
        void b() { a() }
    }
}
@Deprecated @Foo(1)
public final class R2 extends R1 { }
def top() { z() }
