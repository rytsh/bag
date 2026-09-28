class Simple {
    int count = 0
    Simple() {}
    Simple(int c) { count = c }
    public static void main(String[] args) {
        def s = new Simple(1)
        s.inc()
        System.out.println(s.count)
    }
    void inc() { count++; log("x") }
    private static void log(String m) { println m }
}
