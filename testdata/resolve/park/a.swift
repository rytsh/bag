class Svc {
    let net: Network
    func run() {
        net.fetch()
        Remote.call()
        let x = Local()
        x.go()
    }
}
class Local { func go() {} }
