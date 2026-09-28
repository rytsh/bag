package com.x
import com.x.Repo
class Svc extends Base implements Runner {
    Repo repo
    def run(String s) {
        repo.save(s)
        helper()
        println "hi"
    }
    private void helper() {}
}
interface Runner { void run(String s) }
