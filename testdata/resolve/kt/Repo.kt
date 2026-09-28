package com.app.data
class Repo {
  fun save(x: Int) {}
}
object Registry {
  fun lookup(): Int = 1
}
fun topLevel(): Int = 2
