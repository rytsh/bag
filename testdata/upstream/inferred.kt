class Service {
    @Volatile var count = 0
    @Deprecated("old") val name = "service"
    fun run() = count
}
