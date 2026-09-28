package com.x
import spock.lang.Specification
class FooSpec extends Specification {
    def "adds numbers"() {
        expect:
        1 + 1 == 2
    }
    def 'it works'() { when: go() then: true }
    def setup() {}
}
