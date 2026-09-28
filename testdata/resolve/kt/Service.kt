package com.app
import com.app.data.Repo
import com.app.data.topLevel
class Service(private val repo: Repo) {
  fun run() {
    repo.save(1)
    com.app.data.Registry.lookup()
    com.app.data.topLevel()
    topLevel()
  }
}
